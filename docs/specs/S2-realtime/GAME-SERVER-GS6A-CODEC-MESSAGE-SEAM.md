# Game-server GS-6a — codec client/server message seam (implementation spec slice)

| Field | Value |
|---|---|
| Spec ID | SPEC-S2-GAME-SERVER-GS6A (prerequisite slice for GS-6; extends PLAN-S2-CODEC §4.5 cgo seam) |
| Owner | S2 Real-time |
| Status | REVIEW |
| Version | 0.1 |
| Last updated | 2026-10-15 |
| Implements (master IDs) | X-2, NFR-5 (one codec — Go never hand-rolls §4A bytes), NFR-10 (byte-identical across consumers), ADR-14, ADR-15 (in-process C-ABI) |
| Depends on | PLAN-S2-CODEC (RATIFIED — the `codec-cgo` seam + `pgc-protocol` codec); SPEC-S2-PROTOCOL §4A (frozen byte layout); the GS-4a event seam (merged, PR #45/#46 — the proven pattern this mirrors) |
| Gates it feeds | Unblocks GS-6 (the WS transport needs a `§4A client frame → typed ClientMsg` read path and a `ServerMsg → §4A server frame` write path that the current seam does not expose). Makes no lettered gate green by itself. |

> **Why this slice exists — the same gap GS-4 surfaced, on the message path.**
> GS-6's transport must READ a client message's typed fields (`Move{game_id,
> ply_expected, uci}` to route a move) and BUILD a server message
> (`MoveAccepted{…}`, `Matched{…}`) to send. But the `codec-cgo` seam today
> exposes only `pgc_reencode_{client,server,event}(frame) → frame` — a
> **frame→frame** reencode of bytes already in §4A form. There is **no**
> Go-reachable path to decode a `ClientMsg` to typed fields or encode a
> `ServerMsg` from Go fields. The pure `pgc-protocol` crate already HAS
> `encode_client`/`decode_client`/`encode_server`/`decode_server`; only the cgo
> seam never exported a message-level entry point. GS-6a adds exactly that — the
> direct analog of GS-4a's event seam — keeping §4A encoding 100% in Rust
> (NFR-5). No wire bytes change; this adds a seam, it does not alter the frozen
> §4A layout (proven by the still-passing golden-freeze test).

## 1. Goal

Add a `§4A client frame → ClientMsg` decode path and a `ServerMsg → §4A server
frame` encode path to the `codec-cgo` seam and its Go binding, so the GS-6 WS
transport can route client intents and build server replies with typed fields —
without the Go tier ever hand-encoding a §4A byte (NFR-5). Done = the new seam
round-trips every §4A message type byte-identically to the committed golden
corpus, survives the existing ABI fuzz pattern, and the Go binding reproduces
every message's frozen frame.

## 2. The two directions (and why only two)

The server **receives** client frames and **sends** server frames, so exactly
two cgo entry points are needed (the opposite directions — encoding a client
frame, decoding a server frame — are the client's job, and the existing
`pgc_reencode_*` already relays either direction as opaque bytes):

- `pgc_decode_client(frame) → postcard(ClientMsg)` — §4A client frame to the
  postcard intermediate the Go transport reads back to typed fields.
- `pgc_encode_server(postcard(ServerMsg)) → frame` — the Go-built intermediate
  to the canonical §4A server frame.

Both reuse the GS-4a `boundary_impl` + the `PGC_ERR_BAD_INTERMEDIATE` status
code (a bad caller-built intermediate on encode is distinct from a corrupt §4A
frame on decode). The intermediate is `postcard(ClientMsg|ServerMsg)` — serde's
variant-index encoding — NOT the §4A frame; the §4A frame is produced only by
`encode_client`/`encode_server`, selected by the frozen §4A.2 type byte.

## 3. Write/read symmetry (the one correctness fix this slice lands)

GS-6a is the first chunk to give a Go caller a server-frame **write** path, which
makes a pre-existing write/read asymmetry reachable: `decode_server` rejects an
out-of-domain `reason`/`termination`/`result_vector`/`error code`, but
`encode_server` validated **nothing** — so a caller could emit a server frame
every conformant client (and the parity reencode) would refuse to decode. This is
the same class as the GS-4a event S1 fix, but on the live wire path, where an
undecodable frame reaches a real client. GS-6a makes `encode_server` **symmetric
with `decode_server`**: it mirrors all four closed-domain guards and refuses to
produce an out-of-contract discriminant, with per-field encode-reject tests. (The
client path has no closed-domain field today — all client payloads are
string/varint/bool — so `encode_client` is already symmetric.)

## 4. Test obligation

Proven against the REAL Rust codec via the shared, Rust-generated golden corpus
(`protocol/vectors/vectors.json`), never hand-reasoned bytes:

- **Client:** for every committed §4A client frame, `pgc_decode_client` yields a
  Rust intermediate; the Go `unmarshal → re-marshal` must byte-match it, and
  `DecodeClient(frame)` must agree. Proves the Go postcard mirror ==
  `encode_client_intermediate` for all 9 §4A.2 C codes.
- **Server:** a Go `ServerMsg` built to the corpus value, run through
  `EncodeServer`, must reproduce the committed §4A server frame byte-for-byte for
  all 7 §4A.2 S codes (the frozen frame is the drift-proof oracle).
- **Edge:** a high `u8` (seat_idx/turn = 200) round-trips through the real seam —
  pinning the postcard u8-is-one-raw-byte boundary that bit GS-4a (u8-as-varint).
- **Symmetry:** per-field encode-reject tests for unknown reason / termination /
  result byte / error code, plus a positive "every frozen discriminant encodes".
- **Freeze:** the §4A golden frames still reencode to themselves (adding nothing
  to the frozen layout) — asserted on both the Rust and Go sides.

## 5. Open questions

None. The design is a direct mirror of the merged GS-4a seam, and the one
decision (symmetric encode) follows the GS-4a S1 precedent. The §4A.2 type
codes, §4A.3 field order, and the enum8 tables are all frozen (SPEC-S2-PROTOCOL
§4A); this slice adds no new wire bytes.
