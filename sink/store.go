package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	pbfeed "github.com/streamingfast/substreams-foundational-store/pb/sf/substreams/foundational-store/feed/v2"
	pbmodel "github.com/streamingfast/substreams-foundational-store/pb/sf/substreams/foundational-store/model/v2"
	pbservice "github.com/streamingfast/substreams-foundational-store/pb/sf/substreams/foundational-store/service/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/anypb"

	pbwallet "github.com/streamingfast/solana-wallet-tracker-example/pb/com/acme/wallet/v1"
)

// storeCmd groups the Hosted Store admin sub-commands. These showcase exactly
// the client code a customer needs to populate and inspect the store.
//
// KEY ENCODING: the store key is the UTF-8 bytes of the base58 wallet address
// (the string you pass on the command line). The Substreams reader uses the same
// convention, so keys written here line up with the lookups done per block.
var storeCmd = &cobra.Command{
	Use:   "store",
	Short: "Interact with the Hosted Store (add / get / remove wallets)",
}

var storeAddCmd = &cobra.Command{
	Use:   "add <address> <label>",
	Short: "Add (or overwrite) a tracked wallet in the Hosted Store via Feed.Set",
	Args:  cobra.ExactArgs(2),
	RunE:  storeAddE,
}

var storeGetCmd = &cobra.Command{
	Use:   "get <address>",
	Short: "Read a wallet entry from the Hosted Store via Store.Get",
	Args:  cobra.ExactArgs(1),
	RunE:  storeGetE,
}

var storeRemoveCmd = &cobra.Command{
	Use:   "remove <address>",
	Short: "Remove a tracked wallet (NOT supported by the v2 Feed API — see notes)",
	Args:  cobra.ExactArgs(1),
	RunE:  storeRemoveE,
}

var storeReadyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Mark the store ready (or not) for reads via Feed.SetReady",
	Long: `Toggle the store's readiness. Until a store is marked ready, Get/GetFirst
return block_reached=false and a Substreams module that queries it waits. Mark it
ready once you have finished populating it.`,
	Args: cobra.NoArgs,
	RunE: storeReadyE,
}

func init() {
	storeCmd.PersistentFlags().String("endpoint", "", "Hosted Store gRPC endpoint, e.g. <deployment-id>.hs.streamingfast.io:443 (required)")
	storeCmd.PersistentFlags().String("token-envvar", "SUBSTREAMS_API_TOKEN", "Env var holding the StreamingFast JWT used as the Bearer token")
	storeCmd.PersistentFlags().Bool("plaintext", false, "Use a plaintext (non-TLS) connection — for a local dev stack only")

	storeGetCmd.Flags().Uint64("block-number", 0, "Block number to query at; must be <= the store's processed height or it returns block_reached=false")
	storeReadyCmd.Flags().Bool("ready", true, "Readiness state to set")

	storeCmd.AddCommand(storeAddCmd)
	storeCmd.AddCommand(storeGetCmd)
	storeCmd.AddCommand(storeRemoveCmd)
	storeCmd.AddCommand(storeReadyCmd)
}

func storeAddE(cmd *cobra.Command, args []string) error {
	address, label := args[0], args[1]

	conn, err := dialStore(cmd)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Wrap the label in a WalletInfo, then in a google.protobuf.Any. anypb.New
	// sets the type URL to type.googleapis.com/<full message name>, which is the
	// Type URL the store was created with.
	value, err := anypb.New(&pbwallet.WalletInfo{Label: label})
	if err != nil {
		return fmt.Errorf("build Any value: %w", err)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	client := pbfeed.NewFeedClient(conn)
	_, err = client.Set(ctx, &pbfeed.SetRequest{
		Entries: &pbmodel.SinkEntries{
			Entries: []*pbmodel.Entry{
				{
					Key:   &pbmodel.Key{Bytes: []byte(address)},
					Value: value,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("Feed.Set: %w", err)
	}

	fmt.Printf("added %s -> %q\n", address, label)
	return nil
}

func storeGetE(cmd *cobra.Command, args []string) error {
	address := args[0]
	blockNumber, _ := cmd.Flags().GetUint64("block-number")

	conn, err := dialStore(cmd)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	client := pbservice.NewStoreClient(conn)
	resp, err := client.Get(ctx, &pbservice.GetRequest{
		BlockNumber: blockNumber,
		Keys:        []*pbmodel.Key{{Bytes: []byte(address)}},
	})
	if err != nil {
		return fmt.Errorf("Store.Get: %w", err)
	}

	if !resp.BlockReached {
		fmt.Printf("block %d not reached by the store yet (or store not marked ready)\n", blockNumber)
		return nil
	}

	entries := resp.GetEntries().GetEntries()
	if len(entries) == 0 {
		fmt.Println("no entry returned")
		return nil
	}

	entry := entries[0]
	switch entry.Code {
	case pbmodel.ResponseCode_RESPONSE_CODE_FOUND:
		label := "<unreadable>"
		if entry.Entry != nil && entry.Entry.Value != nil {
			info := &pbwallet.WalletInfo{}
			if err := entry.Entry.Value.UnmarshalTo(info); err == nil {
				label = info.Label
			}
		}
		fmt.Printf("FOUND %s -> %q\n", address, label)
	case pbmodel.ResponseCode_RESPONSE_CODE_NOT_FOUND:
		fmt.Printf("NOT_FOUND %s\n", address)
	case pbmodel.ResponseCode_RESPONSE_CODE_NOT_FOUND_FINALIZE:
		fmt.Printf("NOT_FOUND (finalized) %s\n", address)
	default:
		fmt.Printf("unexpected response code %s for %s\n", entry.Code, address)
	}
	return nil
}

func storeReadyE(cmd *cobra.Command, _ []string) error {
	ready, _ := cmd.Flags().GetBool("ready")

	conn, err := dialStore(cmd)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	client := pbfeed.NewFeedClient(conn)
	if _, err := client.SetReady(ctx, &pbfeed.SetReadyRequest{Ready: ready}); err != nil {
		return fmt.Errorf("Feed.SetReady: %w", err)
	}

	fmt.Printf("store ready=%t\n", ready)
	return nil
}

func storeRemoveE(cmd *cobra.Command, args []string) error {
	// The v2 remote-feed Feed service exposes only Set and SetReady — there is
	// no Delete RPC, so a key cannot be removed through the public API today.
	//
	// REVISIT / complete here: removal would require either
	//   1. a new Feed.Delete RPC on the server, or
	//   2. a tombstone convention (Set the key to a sentinel "deleted" value and
	//      have the Substreams reader treat that sentinel as absent).
	// Until one exists, this command intentionally fails loudly instead of
	// pretending to remove anything.
	fmt.Fprintf(os.Stderr,
		"remove is not supported: the v2 Feed API has no Delete RPC (key %q left untouched)\n", args[0])
	return fmt.Errorf("remove not supported by the Hosted Store v2 Feed API")
}

// dialStore opens an authenticated gRPC connection to the Hosted Store.
//
// NOTE ON CLIENT LIFETIME: each CLI sub-command dials a fresh connection and
// closes it on return, because a command is a short, one-shot process. In a
// real (non-CLI) application you should do the opposite: build the
// *grpc.ClientConn ONCE at startup and reuse it (and the pbfeed.FeedClient /
// pbservice.StoreClient built from it) for the whole process lifetime. The
// connection is safe for concurrent use, multiplexes calls over a single
// HTTP/2 connection, and amortizes the TLS handshake — so dialing per call (as
// the CLI does here for simplicity) would be wasteful in a long-running service.
func dialStore(cmd *cobra.Command) (*grpc.ClientConn, error) {
	endpoint, _ := cmd.Flags().GetString("endpoint")
	if endpoint == "" {
		return nil, fmt.Errorf("--endpoint is required (e.g. <deployment-id>.hs.streamingfast.io:443)")
	}
	plaintext, _ := cmd.Flags().GetBool("plaintext")

	opts := []grpc.DialOption{}
	if plaintext {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		tokenEnvvar, _ := cmd.Flags().GetString("token-envvar")
		token := os.Getenv(tokenEnvvar)
		if token == "" {
			return nil, fmt.Errorf("no JWT found in env var %q; mint one at https://thegraph.market/api-keys", tokenEnvvar)
		}
		opts = append(opts,
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
			grpc.WithPerRPCCredentials(bearerToken(token)),
		)
	}

	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial hosted store at %q: %w", endpoint, err)
	}
	return conn, nil
}

// bearerToken attaches `authorization: Bearer <jwt>` to every gRPC call.
type bearerToken string

func (t bearerToken) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}

func (t bearerToken) RequireTransportSecurity() bool { return true }
