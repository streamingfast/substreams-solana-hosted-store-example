# Solana Wallet Tracker — Hosted Store example

End-to-end example of a [Hosted Store](https://thegraph.market) (a.k.a.
foundational store) driving a Solana Substreams, with a companion Go sink.

**Use case:** a customer maintains a watchlist of Solana wallets. They fill a
Hosted Store externally with the wallets to watch (key = wallet address, value =
a label). A Substreams reads the store per block and emits only the transactions
that touch a watched wallet. A Go sink consumes that output and logs it.

```
 customer ──Feed.Set──▶ Hosted Store ◀──per-block reads── Substreams ──stream──▶ Go sink ──▶ terminal
 (sink "store add")                      (map_tracked_transactions)              (sink "run")   + cursor.txt
```

## Layout

```
.
├── proto/                     # shared protobuf (tracked.proto)
├── substreams/                # the Substreams (Rust → WASM)
│   ├── substreams.yaml
│   ├── Cargo.toml
│   └── src/lib.rs
└── sink/                      # the Go sink + Hosted Store admin CLI
    ├── run.go                 # consume output, log, persist cursor
    ├── store.go               # store add / get / remove (client examples)
    └── cursor.go
```

## How it works

### Substreams (`substreams/src/lib.rs`)

`map_tracked_transactions` takes two inputs: the Solana `Block` and the Hosted
Store (declared as `foundational-store: <deployment-id>@v0.1.0`). For each block
it runs two passes with a single batched store lookup in between:

1. walk every successful transaction, collect the distinct account addresses it
   touches;
2. one batched `store.get(...)` over all distinct addresses for the block;
3. walk the transactions again and emit those touching a wallet found in the
   store, each with the label read back from the store.

This keeps the work at **one store round-trip per block**, not one per address.

### Key encoding (important)

The store key is the **raw 32-byte Solana pubkey**, not its base58 string form.
You still pass a base58 address on the command line (that is what humans paste);
the `store add` / `store get` commands base58-**decode** it to the 32 raw bytes
before talking to the store, and reject anything that is not a valid 32-byte
address. The Rust reader keys on the same raw bytes (`account.0`) and only
base58-encodes the handful of matched addresses for its output, which keeps
base58 work out of the per-account hot loop.

The value is a customer-owned `com.acme.wallet.v1.WalletInfo { label }` wrapped
in `google.protobuf.Any`, type URL
`type.googleapis.com/com.acme.wallet.v1.WalletInfo`.

## Prerequisites

- `substreams`, `buf`, Rust (`wasm32-unknown-unknown` target), Go 1.24+.
- `buf` is required to generate the Go protobuf bindings the sink imports.
  Install it with one of:
  ```bash
  brew install bufbuild/buf/buf          # macOS
  go install github.com/bufbuild/buf/cmd/buf@latest   # any platform with Go
  ```
  See <https://buf.build/docs/installation> for other methods.
- A StreamingFast API key. Mint one at <https://thegraph.market/api-keys> and
  export it:
  ```bash
  export SUBSTREAMS_API_KEY=<api-key>
  ```
  Both the sink `run` command and the `store` sub-commands accept it (sent as
  the `x-api-key` header). A pre-minted JWT works too via `SUBSTREAMS_API_TOKEN`,
  used only when `SUBSTREAMS_API_KEY` is unset.

## 1. Create the Hosted Store

In [The Graph Market](https://thegraph.market/sinks/new) create a **Hosted
Store** with Type URL `com.acme.wallet.v1.WalletInfo`. Note the **deployment
ID**, then export it once so the snippets below are copy/paste-ready:

```bash
export DEPLOYMENT_ID=<deployment-id>
export STORE_ENDPOINT=$DEPLOYMENT_ID.hs.streamingfast.io:443
```

Also set the same deployment ID in `substreams/substreams.yaml` (replace the
`<deployment-id>` placeholder on the `foundational-store:` input).

> **Wait for the endpoint to come up.** A freshly created store's endpoint is
> not reachable immediately — it usually takes **5 to 10 minutes**. Until then
> any call (or a plain `curl https://$DEPLOYMENT_ID.hs.streamingfast.io`) fails
> with a confusing gateway error like `fault filter abort`. The `store` commands
> below detect that and tell you to wait; just retry after a few minutes.

## 2. Build the sink CLI

The sink imports Go bindings generated from `proto/tracked.proto`. Generate them
**before** the first `go run .`, otherwise the build fails looking for the
missing `pb/` packages:

```bash
cd sink
buf generate ../proto      # generates ./pb (one time, re-run if the proto changes)
go mod tidy
```

## 3. Fill the store with wallets to track

```bash
cd sink
go run . store add 9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM treasury \
  --endpoint $STORE_ENDPOINT
```

> `store remove` is **not** implemented: the v2 Feed API has no Delete RPC. See
> the note in `sink/store.go`.

## 4. Mark the store ready

A store stays **not ready** until you say so. While not ready, `store get` and a
Substreams module reading it get `block_reached=false` (the module waits) — so
you cannot read back what you just `set` until the store is marked ready. Mark it
ready once you have finished populating it:

```bash
cd sink

buf generate ../proto
go run . store ready --endpoint $STORE_ENDPOINT

# flip it back to not-ready (e.g. before a bulk re-load) with:
go run . store ready --ready=false --endpoint $STORE_ENDPOINT
```

## 5. Read back an entry

Now that the store is ready, `store get` returns the entry you added (before
marking it ready this would report `block_reached=false`):

```bash
cd sink
go run . store get 9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM \
  --endpoint $STORE_ENDPOINT --block-number <recent-slot>
```

## 6. Build & run the Substreams

```bash
cd substreams
substreams build
substreams gui ./substreams.yaml map_tracked_transactions -s 320000000 -t +100
```

## 7. Run the sink

The Go bindings were already generated in step 2, so just run it:

```bash
cd sink
go run . run -s 320000000 -t +100
# cursor is written to ./cursor.txt after each block; re-run to resume
```

## Build notes

- `substreams::store::FoundationalStore` requires `substreams = 0.7`. Use
  `substreams-solana 0.15` (the first 0.7-compatible release; 0.14.x pinned
  `substreams = ^0.6`). Both resolve from crates.io — no patch needed.
- Building/running a manifest with a `foundational-store:` input needs a
  `substreams` CLI that supports it (newer than v1.14.x). Build one from the
  `substreams` repo `develop` branch if your installed CLI rejects the field.
- The Go sink depends on the remote-feed (`Feed.Set` / `Feed.SetReady`) API of
  `substreams-foundational-store`, which is not in a tagged release yet, so
  `sink/go.mod` pins an unreleased pseudo-version of it rather than a `vX.Y.Z`
  tag. Bump it to the release once that API ships.
