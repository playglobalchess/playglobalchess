# protocol/vectors — frozen golden-vector corpus

The committed, cross-language **golden vectors** for the `pgc-protocol` wire +
event codec (ADR-14). Each vector is a `(value, bytes)` pair: a canonical message
or event and the exact bytes the frozen SPEC-S2-PROTOCOL §4A codec produces for
it. They are the single source of truth that every consumer of the codec —
Rust, and later the Swift/Kotlin (UniFFI) and web (wasm) bindings and the Go
server — is checked against, byte-for-byte (SPEC-S2-PROTOCOL §8, NFR-10).

## Why they exist

`postcard` (the ADR-14 / PLAN-CODEC §3 codec) is **positional**: it identifies a
field by its order, not a tag. So the §4A field order *is* the wire contract, and
an accidental field reorder/insert silently changes the bytes with no error.
These vectors are the guard: the `golden_freeze` test replays every pair and
asserts the live codec still reproduces the committed `hex` exactly, turning any
such reorder into a **loud CI failure** instead of a silent wire break. The
vectors *are* the freeze enforcement (PLAN-CODEC §3).

## Format — `vectors.json`

```jsonc
{
  "schema": 1,
  "note": "...",
  "vectors": [
    { "name": "client/move", "kind": "client", "bytes": 45, "hex": "0103..." }
    // ...
  ]
}
```

| Field  | Meaning |
|--------|---------|
| `name` | Stable identifier, `kind/case` (e.g. `server/game_end`). |
| `kind` | Which codec entry point produced it: `client` (`encode_client`), `server` (`encode_server`), or `event` (`encode_event`). |
| `bytes`| Length of the encoded frame, for a quick human sanity check. |
| `hex`  | The exact encoded bytes, lower-case hex. **This is the frozen value.** |

Hex (not base64) so a reviewer can read the frame envelope by eye: byte 0 is
`ver` (`01`), byte 1 is the §4A.2 `type` code, and the rest is the
postcard-encoded payload in §4A.3 / §4A.8 field order. (Event vectors have no
`ver`/direction byte — their envelope is `stream_id, seq, type, schema_ver, ts,
body` per §4A.8.)

## Coverage

Every §4A message type and every §4A.9 event type appears at least once, plus the
PLAN-CODEC §4.2 edge values: a max-length FEN in `STATE`, a 5-char promotion UCI
(`e7e8q`), an N-seat-ready `result_vector`, a daily per-move time-control id, the
`0xFF` system-abort sentinel, and a `GameEnded` carrying a full `termination`
enum. The canonical source list is `codec/tests/corpus/mod.rs`.

## Regenerating (deliberate changes only)

```sh
cd protocol/codec
cargo run --example gen_vectors   # rewrites ../vectors/vectors.json
```

Regenerate **only** when you are intentionally changing the wire format (and that
change has gone through the §4.2 contract change-control: both affected streams +
architecture review). An *accidental* change shows up first as a failing
`golden_freeze` test — do not "fix" that by regenerating; fix the code. After G-D
freeze a wire change is a new protocol version, not an in-place edit
(SPEC-S2-PROTOCOL §4.2, §5).
