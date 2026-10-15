# pgc-protocol-wasm — the web codec binding (ADR-14, SPEC-S4-WEB-F01 §3.0)

Compiles the pure `pgc-protocol` §4A codec to wasm so the web client runs the
**exact same** encode/decode as the server (cgo) and mobile (UniFFI) — parity by
construction (NFR-5, NFR-10). This crate adds **no codec logic**; it only
re-exposes `pgc-protocol` to JS/wasm.

## Surface

- `reencode_{client,server,event}(bytes) -> bytes` — the byte→byte parity
  primitive (decode then re-encode, entirely in Rust).
- **S4a typed message seam** — `encodeClient(msg) -> Uint8Array` /
  `decodeServer(frame) -> msg`, plus the `encodeServer`/`decodeClient` mirrors
  for tests. The §4A FRAME is produced only by `pgc_protocol::encode_*` in Rust;
  the typed `ClientMsg`/`ServerMsg` cross the JS boundary via `serde-wasm-bindgen`.

### TS contract notes (what a caller must send/expect)

- Messages are **externally tagged**: `{"Seek": {pool_id, time_control, rated}}`,
  `{"Move": {game_id, ply_expected, uci}}`, etc.
- The id newtypes (`GameId`/`PoolId`/`TimeControlId`) are `#[serde(transparent)]`
  → **bare strings** on the JS side.
- The §4A enum8 fields (`reason`, `termination`, `code`) are transparent over
  `u8` → **bare numbers** (e.g. `termination: 10`), never a string or tagged object.
- **`u64` wire fields (`Ping.t`, `Pong.t`, `Pong.server_t`) decode as JS
  `BigInt`** (the serializer sets `serialize_large_number_types_as_bigints`), so
  the whole u64 domain is lossless above `Number.MAX_SAFE_INTEGER`. The TS type
  for a u64 wire field is `bigint`. A plain JS number is still accepted on encode
  while ≤ 2^53−1.

## Build & test

```
# fmt + clippy on the wasm target
cargo fmt   --manifest-path protocol/codec-wasm/Cargo.toml --all -- --check
cargo clippy --manifest-path protocol/codec-wasm/Cargo.toml --all-targets \
  --target wasm32-unknown-unknown -- -D warnings

# node parity test (the shipped artifact, against the shared golden corpus)
( cd protocol/codec-wasm && wasm-pack test --node )
```

### Local-dev gotcha (sandboxed macOS dev box only)

On the sandboxed dev box, `$TMPDIR` points inside `$KIROCREW_SCRATCH`, and the
node wasm test runner's `realpathSync` **EPERMs** walking that tree, so
`wasm-pack test --node` fails before running a single test. Run it with `TMPDIR`
pointed at a project-local directory:

```
( cd protocol/codec-wasm && mkdir -p .wasmtest-tmp \
  && TMPDIR="$PWD/.wasmtest-tmp" wasm-pack test --node )
```

CI (ubuntu) is unaffected — no override is needed there.

## pkg-node

`pkg-node/` is a **generated build artifact** (gitignored). Regenerate with
`wasm-pack build --target nodejs --out-dir pkg-node`. The web app consumes the
published package; the committed source of truth is this crate.
