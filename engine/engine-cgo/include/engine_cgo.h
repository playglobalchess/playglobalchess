/* engine_cgo.h — C-ABI for the PlayGlobalChess S1 engine (X-1, ADR-11, ADR-15,
 * SPEC-S2-GAME-SERVER-GS2). Hand-written (surface is tiny; the Go contract test
 * linking it is the proof it stays in sync with src/lib.rs). Byte/scalar-
 * oriented: strings cross as (ptr,len) UTF-8; the one output buffer Rust
 * allocates and the caller copies then frees via pgc_engine_free.
 */
#ifndef PGC_ENGINE_CGO_H
#define PGC_ENGINE_CGO_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Status codes. */
#define PGC_ENG_OK          0
#define PGC_ENG_ERR_FEN     1   /* invalid FEN */
#define PGC_ENG_ERR_MOVE    2   /* unparseable / malformed UCI */
#define PGC_ENG_ERR_ILLEGAL 3   /* parsed but not legal in this position */
#define PGC_ENG_ERR_NULL_ARG 98 /* a null/!0-length-null argument */
#define PGC_ENG_ERR_PANIC    99 /* a panic caught at the boundary (never UB) */

/* out_status values (closed enum, 1:1 with pgc_engine::GameStatus). */
#define PGC_ENG_STATUS_ONGOING      0
#define PGC_ENG_STATUS_CHECKMATE    1
#define PGC_ENG_STATUS_STALEMATE    2
#define PGC_ENG_STATUS_INSUFFICIENT 3
#define PGC_ENG_STATUS_FIFTYMOVE    4
#define PGC_ENG_STATUS_REPETITION   5

/* out_claim values (closed enum, 1:1 with pgc_engine::DrawClaim; NONE for the
 * non-draw statuses). */
#define PGC_ENG_CLAIM_NONE      0
#define PGC_ENG_CLAIM_CLAIMABLE 1
#define PGC_ENG_CLAIM_AUTOMATIC 2

/* Apply one UCI move to a FEN.
 *
 * Out-parameter write-conditions (SPEC §4): all five out-params are written
 * ONLY on PGC_ENG_OK, and NONE of them on any non-OK return. The caller must
 * treat them as untouched on error and read none of them.
 *   On PGC_ENG_OK:
 *     *out_fen / *out_fen_len = the position AFTER the move (copy, then free
 *                               with pgc_engine_free);
 *     *out_status  = one of PGC_ENG_STATUS_* for the resulting position;
 *     *out_claim   = one of PGC_ENG_CLAIM_* (NONE unless out_status is a draw);
 *     *out_zobrist = the Zobrist key of the resulting position (so the server
 *                    appends it to its folded history without re-hashing in Go).
 * PGC_ENG_ERR_MOVE  = UCI unparseable. PGC_ENG_ERR_ILLEGAL = parsed but illegal
 * in this position (no out buffer; the pipeline emits MOVE_REJECTED{ILLEGAL}).
 *
 * prior_keys (ptr,len) is the Zobrist history the server folded, EXCLUDING the
 * resulting position (status_with_history's convention). prior_keys may be NULL
 * iff prior_keys_len == 0. fen/uci may NOT be NULL unless their len is 0. */
int32_t pgc_engine_apply(const uint8_t *fen, size_t fen_len,
                         const uint8_t *uci, size_t uci_len,
                         const uint64_t *prior_keys, size_t prior_keys_len,
                         uint8_t **out_fen, size_t *out_fen_len,
                         int32_t *out_status, int32_t *out_claim,
                         uint64_t *out_zobrist);

/* Per-side mating-capacity query for the flag branch (SPEC-S2-GAME-SERVER §3.3,
 * FIDE 6.9). color_idx: 0 = White, 1 = Black (the engine's Color index; v1
 * seat->color mapping is the caller's). On PGC_ENG_OK, *out_can_mate is 1 if
 * that side can checkmate by some series of legal moves, else 0. On non-OK,
 * *out_can_mate is NOT written. */
int32_t pgc_engine_mating_material(const uint8_t *fen, size_t fen_len,
                                   uint8_t color_idx, int32_t *out_can_mate);

/* Free a buffer handed out by a pgc_engine_apply success (same ptr+len).
 * NULL = no-op. */
void pgc_engine_free(uint8_t *ptr, size_t len);

/* Engine ABI version, so the Go side can assert it links the expected build. */
uint32_t pgc_engine_abi_version(void);

#ifdef __cplusplus
}
#endif

#endif /* PGC_ENGINE_CGO_H */
