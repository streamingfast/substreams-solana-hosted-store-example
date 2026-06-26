# Solana Wallet Tracker Substreams

Reports the Solana transactions that touch a wallet present in a
[Hosted Store](https://thegraph.market). The store is filled externally (e.g. by
the companion sink's `store add` command) with the wallets to watch.

The single module `map_tracked_transactions` takes the Solana `Block` and the
Hosted Store as inputs and, for each block, emits only the transactions that
touch a watched wallet — each match carrying the label read back from the store.

Store keys are the raw 32-byte Solana pubkeys; lookups are batched to one store
round-trip per block. See the repository
[README](https://github.com/streamingfast/solana-wallet-tracker-example) for the
end-to-end walkthrough (creating the store, filling it, and running the sink).
