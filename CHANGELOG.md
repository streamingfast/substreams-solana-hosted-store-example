# Changelog

All notable changes to this example are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## Unreleased

### Added

- The `store` sub-commands now accept a StreamingFast API key via
  `SUBSTREAMS_API_KEY` (sent as the `x-api-key` header, `--api-key-envvar` to
  override), matching the sink `run` command. A `SUBSTREAMS_API_TOKEN` JWT is
  still accepted as a fallback when no API key is set.

### Changed

- The `store` commands now build their Hosted Store gRPC connection through
  dgrpc's external-client helpers (round-robin, keepalive, OpenTelemetry, proper
  TLS) instead of a hand-rolled `grpc.NewClient` with a bare TLS config, so the
  connection matches what the sink `run` command uses.

### Fixed

- Cleared the Substreams packaging warnings: replaced the deprecated
  `package.doc` field with a `substreams/README.md` (picked up as the package
  doc), set `package.url` and `package.description`, and declared protobuf
  `excludePaths` for the bundled `google` and
  `sf/substreams/foundational-store` protos (scoped narrowly so the package's
  own `sf/substreams/example` proto is still generated).
- The Go sink now builds standalone after a plain clone. `sink/go.mod` no longer
  carries a local-path `replace` for `substreams-foundational-store` (which only
  resolved when the example sat next to that repo); it pins a fetchable
  pseudo-version of the dependency instead.

### Changed

- Store key is now the raw 32-byte Solana pubkey instead of the UTF-8 bytes of
  the base58 address. The `store add` / `store get` commands still take a base58
  address but now base58-decode it (and validate it is a 32-byte address) before
  talking to the store; the Substreams reader keys on the same raw bytes and
  base58-encodes only matched addresses for its output.
