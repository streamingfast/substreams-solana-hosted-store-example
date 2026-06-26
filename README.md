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

## 2. Fill the store with wallets to track

```bash
cd sink
go run . store add 9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM treasury \
  --endpoint $STORE_ENDPOINT

# inspect it back
go run . store get 9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM \
  --endpoint $STORE_ENDPOINT --block-number <recent-slot>
```

> `store remove` is **not** implemented: the v2 Feed API has no Delete RPC. See
> the note in `sink/store.go`.

## 3. Mark the store ready

A store stays **not ready** until you say so. While not ready, `store get` and a
Substreams module reading it get `block_reached=false` (the module waits), so
mark it ready once you have finished populating it:

```bash
cd sink
go run . store ready --endpoint $STORE_ENDPOINT

# flip it back to not-ready (e.g. before a bulk re-load) with:
go run . store ready --ready=false --endpoint $STORE_ENDPOINT
```

## 4. Build & run the Substreams

```bash
cd substreams
substreams build
substreams gui ./substreams.yaml map_tracked_transactions -s 320000000 -t +100
```

## 5. Run the sink

```bash
cd sink
buf generate ../proto      # generate Go bindings for tracked.proto (one time)
go mod tidy
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
