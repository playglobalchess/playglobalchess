/* pgc_protocol_cgo.h — C-ABI for the PlayGlobalChess wire + event codec.
 *
 * ADR-14 / ADR-15 / PLAN-S2-CODEC v1.1 §4.5. Hand-written (surface is tiny; the
 * Go contract test linking it is the proof it stays in sync with src/lib.rs).
 * Byte-oriented (Q-GS1): the reencode_* calls decode-then-re-encode a frame
 * entirely in Rust and return the canonical bytes.
 *
 * Memory: a successful reencode_* writes a Rust-allocated buffer into
 * *out / *out_len. The caller MUST copy it and then release it with
 * pgc_free(*out, *out_len) exactly once. The caller never frees it any other
 * way and never passes a pointer Rust will retain.
 */
#ifndef PGC_PROTOCOL_CGO_H
#define PGC_PROTOCOL_CGO_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Status codes. 0 = success; 1..6 mirror pgc_protocol::CodecError;
 * 97 = a bad GS-4a boundary intermediate handed to pgc_encode_event;
 * 98 = a null/!0-length-null argument; 99 = a panic caught at the boundary. */
#define PGC_OK 0
#define PGC_ERR_TOO_SHORT 1
#define PGC_ERR_UNSUPPORTED_VERSION 2
#define PGC_ERR_UNKNOWN_TYPE 3
#define PGC_ERR_WRONG_DIRECTION 4
#define PGC_ERR_TOO_LARGE 5
#define PGC_ERR_MALFORMED 6
#define PGC_ERR_BAD_INTERMEDIATE 97
#define PGC_ERR_NULL_ARG 98
#define PGC_ERR_PANIC 99

/* Decode a client->server frame and re-encode it. On PGC_OK, the out-params
 * (out, out_len) hold a buffer to copy then free with pgc_free. buf may be NULL
 * iff len==0. */
int32_t pgc_reencode_client(const uint8_t *buf, size_t len,
                            uint8_t **out, size_t *out_len);

/* Decode a server->client frame and re-encode it. See pgc_reencode_client. */
int32_t pgc_reencode_server(const uint8_t *buf, size_t len,
                            uint8_t **out, size_t *out_len);

/* Decode a stored event and re-encode it. See pgc_reencode_client. */
int32_t pgc_reencode_event(const uint8_t *buf, size_t len,
                           uint8_t **out, size_t *out_len);

/* GS-4a event seam. pgc_encode_event: IN = the postcard intermediate (an
 * EventEnvelope the Go server built), OUT = the canonical PROTOCOL-4A event
 * frame. A bad intermediate returns PGC_ERR_BAD_INTERMEDIATE. Keeps 4A encoding
 * entirely in Rust (NFR-5). Out-params/ownership as pgc_reencode_client. */
int32_t pgc_encode_event(const uint8_t *buf, size_t len,
                         uint8_t **out, size_t *out_len);

/* GS-4a event seam. pgc_decode_event: IN = a stored 4A event frame, OUT = the
 * postcard intermediate the Go server reads. Inverse of pgc_encode_event. See
 * pgc_reencode_client for out-params and ownership. */
int32_t pgc_decode_event(const uint8_t *buf, size_t len,
                         uint8_t **out, size_t *out_len);

/* Free a buffer handed out by a reencode_* success (same ptr+len). NULL = no-op. */
void pgc_free(uint8_t *ptr, size_t len);

/* Protocol major version (§4A.1). */
uint8_t pgc_protocol_version(void);

#ifdef __cplusplus
}
#endif

#endif /* PGC_PROTOCOL_CGO_H */
