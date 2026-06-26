use std::collections::{HashMap, HashSet};

use prost::Message; // brings `WalletInfo::decode` into scope
use substreams::errors::Error;
use substreams::pb::sf::substreams::foundational_store::model::v2::ResponseCode;
use substreams::store::FoundationalStore;
use substreams_solana::base58;
use substreams_solana::pb::sf::solana::r#type::v1::Block;

mod pb;
use pb::com::acme::wallet::v1::WalletInfo;
use pb::sf::substreams::example::wallettracker::v1::{
    TrackedMatch, TrackedTransaction, TrackedTransactions,
};

/// Reports the transactions in `block` that touch at least one wallet present in
/// the Hosted Store.
///
/// The work is done in two passes, with a single batched store lookup in
/// between, so we query the Hosted Store once per block instead of once per
/// candidate address:
///
///   1. walk every successful transaction and collect the distinct account
///      addresses it touches (block-wide dedup feeds the batch lookup);
///   2. one batched `store.get(...)` over all distinct addresses;
///   3. walk the transactions again and emit those that touched a found wallet.
#[substreams::handlers::map]
fn map_tracked_transactions(
    block: Block,
    store: FoundationalStore,
) -> Result<TrackedTransactions, Error> {
    // --- Pass 1: collect candidate addresses --------------------------------
    // Store keys are the RAW 32-byte Solana pubkeys, NOT their base58 string
    // form, so we dedup and look up on the raw bytes and base58-encode only the
    // handful of matched addresses for human-readable output (pass 2). This
    // keeps base58 encoding out of the per-account hot loop.
    //
    // Per-transaction list of touched addresses (raw bytes), preserving which
    // address belongs to which transaction for pass 2.
    let mut per_tx: Vec<(String, Vec<Vec<u8>>)> = Vec::new();
    // Block-wide distinct addresses, in stable order, for the batch lookup.
    let mut distinct: Vec<Vec<u8>> = Vec::new();
    let mut block_seen: HashSet<Vec<u8>> = HashSet::new();

    for trx in block.transactions() {
        let signature = trx.id();

        let mut addresses: Vec<Vec<u8>> = Vec::new();
        let mut tx_seen: HashSet<Vec<u8>> = HashSet::new();

        for instruction in trx.walk_instructions() {
            for account in instruction.accounts() {
                // account.0 is the raw &Vec<u8> pubkey; no base58 encode here.
                let address = account.0.to_vec();
                if tx_seen.insert(address.clone()) {
                    addresses.push(address.clone());
                }
                if block_seen.insert(address.clone()) {
                    distinct.push(address);
                }
            }
        }

        if !addresses.is_empty() {
            per_tx.push((signature, addresses));
        }
    }

    // --- Batched Hosted Store lookup ----------------------------------------
    // Keys are the raw 32-byte pubkeys collected above.
    let keys: Vec<&[u8]> = distinct.iter().map(Vec::as_slice).collect();

    // raw pubkey -> label, only for wallets actually present in the store.
    let mut found: HashMap<Vec<u8>, String> = HashMap::new();
    if !keys.is_empty() {
        let response = store.get(&keys);

        // Entries come back one per requested key, in the same order.
        for (index, entry) in response.entries.iter().enumerate() {
            if entry.code != ResponseCode::Found as i32 {
                continue;
            }

            let label = entry
                .entry
                .as_ref()
                .and_then(|e| e.value.as_ref())
                .map(|any| decode_label(&any.value))
                .unwrap_or_default();

            found.insert(distinct[index].clone(), label);
        }
    }

    // --- Pass 2: emit transactions that matched -----------------------------
    let mut output = TrackedTransactions {
        slot: block.slot,
        transactions: Vec::new(),
    };

    for (signature, addresses) in per_tx {
        let matches: Vec<TrackedMatch> = addresses
            .into_iter()
            .filter_map(|address| {
                found.get(&address).map(|label| TrackedMatch {
                    // base58-encode only the matched addresses, for output.
                    address: base58::encode(&address),
                    label: label.clone(),
                })
            })
            .collect();

        if !matches.is_empty() {
            output.transactions.push(TrackedTransaction { signature, matches });
        }
    }

    Ok(output)
}

/// Decodes the Hosted Store value (the inner bytes of the stored
/// `google.protobuf.Any`) into a label.
///
/// The store value for this example is a `WalletInfo`. We fall back to a lossy
/// UTF-8 view if the bytes are not a valid `WalletInfo`, so a misconfigured
/// store still yields something human-readable instead of failing the block.
fn decode_label(any_value: &[u8]) -> String {
    match WalletInfo::decode(any_value) {
        Ok(info) => info.label,
        Err(_) => String::from_utf8_lossy(any_value).into_owned(),
    }
}
