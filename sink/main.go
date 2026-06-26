package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/streamingfast/logging"
)

// zlog/tracer follow the StreamingFast convention: one PackageLogger per
// package, instantiated once in main via logging.InstantiateLoggers. Set
// `DLOG=debug` (or `.*=debug`) to see debug output.
var zlog, tracer = logging.PackageLogger("wallet-tracker-sink", "github.com/streamingfast/solana-wallet-tracker-example")

var rootCmd = &cobra.Command{
	Use:   "wallet-tracker-sink",
	Short: "Companion sink + Hosted Store admin for the Solana wallet tracker example",
	Long: `Companion CLI for the Solana wallet tracker Substreams example.

It does two jobs:

  run            consume the Substreams output and log tracked transactions to
                 the terminal, persisting a cursor to disk for resumability.

  store add/get  interact with the Hosted Store over gRPC. These mirror the code
                 a customer writes to fill the store (add) and inspect it (get).
                 They double as a demo tool and as copy-paste client examples.`,
}

func init() {
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(storeCmd)
}

func main() {
	logging.InstantiateLoggers()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
