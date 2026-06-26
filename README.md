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

The store key is the **UTF-8 bytes of the base58 wallet address** (not the raw
32-byte pubkey). The Rust reader and the `store add` command both follow this,
so keys line up. The value is a customer-owned `com.acme.wallet.v1.WalletInfo
{ label }` wrapped in `google.protobuf.Any`, type URL
`type.googleapis.com/com.acme.wallet.v1.WalletInfo`.

## Prerequisites

- `substreams`, `buf`, Rust (`wasm32-unknown-unknown` target), Go 1.24+.
- A StreamingFast API token (JWT). Mint one at
  <https://thegraph.market/api-keys> and export it:
  ```bash
  export SUBSTREAMS_API_TOKEN=<jwt>
  ```

## 1. Create the Hosted Store

In [The Graph Market](https://thegraph.market/sinks/new) create a **Hosted
Store** with Type URL
`com.acme.wallet.v1.WalletInfo`. Note the **deployment ID**,
then set it in `substreams/substreams.yaml` (replace `<deployment-id>`).

The store's gRPC endpoint is `<deployment-id>.hs.streamingfast.io:443`.

## 2. Fill the store with wallets to track

```bash
cd sink
go run . store add 9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM treasury \
  --endpoint <deployment-id>.hs.streamingfast.io:443

# inspect it back
go run . store get 9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM \
  --endpoint <deployment-id>.hs.streamingfast.io:443 --block-number <recent-slot>
```

> `store remove` is **not** implemented: the v2 Feed API has no Delete RPC. See
> the note in `sink/store.go`.

## 3. Build & run the Substreams

```bash
cd substreams
substreams build
substreams gui ./substreams.yaml map_tracked_transactions -s 320000000 -t +100
```

## 4. Run the sink

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
