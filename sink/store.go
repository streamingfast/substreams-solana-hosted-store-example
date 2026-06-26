package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mr-tron/base58"
	"github.com/spf13/cobra"
	"github.com/streamingfast/dgrpc"
	"github.com/streamingfast/substreams/client"
	pbfeed "github.com/streamingfast/substreams-foundational-store/pb/sf/substreams/foundational-store/feed/v2"
	pbmodel "github.com/streamingfast/substreams-foundational-store/pb/sf/substreams/foundational-store/model/v2"
	pbservice "github.com/streamingfast/substreams-foundational-store/pb/sf/substreams/foundational-store/service/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/anypb"

	pbwallet "github.com/streamingfast/solana-wallet-tracker-example/pb/com/acme/wallet/v1"
)

// storeCmd groups the Hosted Store admin sub-commands. These showcase exactly
// the client code a customer needs to populate and inspect the store.
//
// KEY ENCODING: the address you pass on the command line is a base58 Solana
// address, but the store key is the RAW 32-byte pubkey it decodes to — NOT the
// base58 string. decodeAddress below does that conversion (and validates the
// input), and the Substreams reader keys on the same raw bytes, so entries
// written here line up with the lookups done per block.
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
	storeCmd.PersistentFlags().String("api-key-envvar", "SUBSTREAMS_API_KEY", "Env var holding a StreamingFast API key (sent as the x-api-key header); preferred, takes precedence over --token-envvar")
	storeCmd.PersistentFlags().String("token-envvar", "SUBSTREAMS_API_TOKEN", "Env var holding a StreamingFast JWT (sent as the Bearer token); used only when the API key env var is empty")
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

	key, err := decodeAddress(address)
	if err != nil {
		return err
	}

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
					Key:   &pbmodel.Key{Bytes: key},
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

	key, err := decodeAddress(address)
	if err != nil {
		return err
	}

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
		Keys:        []*pbmodel.Key{{Bytes: key}},
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

// decodeAddress turns a base58 Solana address (what a human pastes on the
// command line) into the RAW 32-byte pubkey used as the store key. It rejects
// anything that is not valid base58 or does not decode to exactly 32 bytes, so a
// typo'd address fails loudly here instead of being stored under a bogus key
// that the Substreams reader would never match.
func decodeAddress(address string) ([]byte, error) {
	raw, err := base58.Decode(address)
	if err != nil {
		return nil, fmt.Errorf("invalid base58 Solana address %q: %w", address, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("invalid Solana address %q: decoded to %d bytes, expected 32", address, len(raw))
	}
	return raw, nil
}

// dialStore opens an authenticated gRPC connection to the Hosted Store, built
// the same way the sink `run` command builds its Substreams connection: through
// dgrpc's external-client helpers (round-robin, keepalive, OpenTelemetry, large
// receive limit and proper TLS), with the transport credentials selected by
// dgrpc from the --plaintext flag.
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

	transportCreds, err := dgrpc.WithAutoTransportCredentials(false, plaintext, false)
	if err != nil {
		return nil, fmt.Errorf("configure transport credentials: %w", err)
	}
	opts := []grpc.DialOption{transportCreds}

	// Auth is only attached over a secure transport; plaintext is for a local
	// dev stack that needs no credentials.
	if !plaintext {
		creds, err := perRPCCredentials(cmd)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithPerRPCCredentials(creds))
	}

	conn, err := dgrpc.NewClientConn(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial hosted store at %q: %w", endpoint, err)
	}
	return conn, nil
}

// perRPCCredentials resolves the call credential the same way the sink's `run`
// command does: a StreamingFast API key (sent as x-api-key) is preferred, and a
// JWT (sent as a Bearer token) is the fallback when no API key is set.
func perRPCCredentials(cmd *cobra.Command) (credentials.PerRPCCredentials, error) {
	apiKeyEnvvar, _ := cmd.Flags().GetString("api-key-envvar")
	if key := os.Getenv(apiKeyEnvvar); key != "" {
		return apiKeyCredential(key), nil
	}

	tokenEnvvar, _ := cmd.Flags().GetString("token-envvar")
	if token := os.Getenv(tokenEnvvar); token != "" {
		return bearerToken(token), nil
	}

	return nil, fmt.Errorf(
		"no credential found: set %s (a StreamingFast API key, preferred) or %s (a JWT); mint one at https://thegraph.market/api-keys",
		apiKeyEnvvar, tokenEnvvar)
}

// apiKeyCredential attaches `x-api-key: <key>` to every gRPC call. The Hosted
// Store gateway accepts a StreamingFast API key directly, exactly like the
// Substreams endpoint the `run` command talks to.
type apiKeyCredential string

func (k apiKeyCredential) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{client.ApiKeyHeader: string(k)}, nil
}

func (k apiKeyCredential) RequireTransportSecurity() bool { return true }

// bearerToken attaches `authorization: Bearer <jwt>` to every gRPC call.
type bearerToken string

func (t bearerToken) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}

func (t bearerToken) RequireTransportSecurity() bool { return true }
