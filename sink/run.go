package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	pbsubstreamsrpc "github.com/streamingfast/substreams/pb/sf/substreams/rpc/v2"
	"github.com/streamingfast/substreams/sink"
	"go.uber.org/zap"

	pbtracked "github.com/streamingfast/solana-wallet-tracker-example/pb/sf/substreams/example/wallettracker/v1"
)

// expectedOutputModuleType guards against pointing the sink at a module whose
// output is not TrackedTransactions.
var expectedOutputModuleType = string(new(pbtracked.TrackedTransactions).ProtoReflect().Descriptor().FullName())

var runCmd = &cobra.Command{
	Use:   "run [<manifest> [<output_module>]]",
	Short: "Consume the Substreams output and log tracked transactions",
	Long: `Consume the wallet-tracker Substreams and log every tracked transaction to
the terminal. The stream cursor is saved to disk after each block so the sink
resumes where it left off on restart.

Defaults: manifest "../substreams/substreams.yaml", module "map_tracked_transactions".`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runE,
}

func init() {
	// Adds --endpoint, --start-block, --stop-block, --api-key-envvar, etc.
	sink.AddFlagsToSet(runCmd.Flags())
	runCmd.Flags().String("cursor-file", "cursor.txt", "Path to the file used to persist the stream cursor")
}

func runE(cmd *cobra.Command, args []string) error {
	manifestPath := "../substreams/substreams.yaml"
	outputModuleName := "map_tracked_transactions"
	if len(args) > 0 {
		manifestPath = args[0]
	}
	if len(args) > 1 {
		outputModuleName = args[1]
	}

	cursorFile, _ := cmd.Flags().GetString("cursor-file")

	sinker, err := sink.NewFromViper(
		cmd,
		expectedOutputModuleType,
		manifestPath,
		outputModuleName,
		"wallet-tracker-sink/0.1.0",
		zlog,
		tracer,
	)
	if err != nil {
		return fmt.Errorf("unable to create sinker: %w", err)
	}

	sinker.OnTerminating(func(err error) {
		if err != nil {
			zlog.Error("sinker terminated with error", zap.Error(err))
		}
	})

	cursor, err := loadCursor(cursorFile)
	if err != nil {
		return fmt.Errorf("load cursor: %w", err)
	}

	handlers := sink.NewSinkerHandlers(
		func(ctx context.Context, data *pbsubstreamsrpc.BlockScopedData, isLive *bool, c *sink.Cursor) error {
			return handleBlockScopedData(ctx, cursorFile, data, isLive, c)
		},
		func(ctx context.Context, undo *pbsubstreamsrpc.BlockUndoSignal, c *sink.Cursor) error {
			return handleBlockUndoSignal(ctx, cursorFile, undo, c)
		},
	)

	// Blocking call; runs until the stream ends or the process is terminated.
	sinker.Run(context.Background(), cursor, handlers)
	return nil
}

func handleBlockScopedData(
	_ context.Context,
	cursorFile string,
	data *pbsubstreamsrpc.BlockScopedData,
	_ *bool,
	cursor *sink.Cursor,
) error {
	tracked := &pbtracked.TrackedTransactions{}
	if err := data.Output.MapOutput.UnmarshalTo(tracked); err != nil {
		return fmt.Errorf("unmarshal output: %w", err)
	}

	// This is the "complete here" point: a real sink would write to its
	// destination (DB, queue, ...) inside a transaction together with the
	// cursor. Here we just log to the terminal.
	for _, trx := range tracked.Transactions {
		parts := make([]string, 0, len(trx.Matches))
		for _, m := range trx.Matches {
			parts = append(parts, fmt.Sprintf("%s=%q", m.Address, m.Label))
		}
		fmt.Printf("slot=%d signature=%s matched=[%s]\n", tracked.Slot, trx.Signature, strings.Join(parts, " "))
	}

	// RULE: persist the cursor only AFTER the block was processed successfully.
	if err := persistCursor(cursorFile, cursor); err != nil {
		return fmt.Errorf("persist cursor: %w", err)
	}
	return nil
}

func handleBlockUndoSignal(
	_ context.Context,
	cursorFile string,
	undo *pbsubstreamsrpc.BlockUndoSignal,
	cursor *sink.Cursor,
) error {
	// REVISIT / complete here: on a reorg a real sink must delete or revert any
	// data it already emitted for blocks above undo.LastValidBlock, in the same
	// atomic unit as the cursor write. We only log to the terminal, so there is
	// nothing to roll back — we just record the rewind and re-persist the cursor.
	zlog.Warn("reorg detected, rewinding",
		zap.Uint64("last_valid_block", undo.LastValidBlock.Number),
		zap.String("last_valid_block_id", undo.LastValidBlock.Id),
	)

	if err := persistCursor(cursorFile, cursor); err != nil {
		return fmt.Errorf("persist cursor: %w", err)
	}
	return nil
}
