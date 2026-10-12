// Package codec is the Go game-server's in-process binding to the one
// PlayGlobalChess wire + event codec (ADR-14, ADR-15, PLAN-S2-CODEC v1.1 §4.5).
// It links the pgc-protocol-cgo staticlib via cgo and calls the SAME encode/
// decode the server-core, mobile (UniFFI) and web (wasm) run — so the Go tier
// never hand-rolls §4A bytes (NFR-5) and is byte-identical to every other
// consumer (NFR-10). This package adds no codec logic; it is the Go leg of the
// one codec.
//
// The staticlib (libpgc_protocol_cgo.a) is produced by `make lib` (see the
// Makefile) and staged into ./lib/ — a gitignored build output the cgo LDFLAGS
// below resolve against. The C header is NOT copied: cgo includes it directly
// from the canonical crate source (protocol/codec-cgo/include) via the CFLAGS
// -I below, so there is no second header copy to drift out of sync.
//
// If you run `go build/test/vet ./...` directly and see a linker error like
// "library 'pgc_protocol_cgo' not found", the staticlib has not been built yet
// — run `make lib` (or `make test`) first. CI runs `make test`, which builds it.
package codec

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../protocol/codec-cgo/include
#cgo LDFLAGS: -L${SRCDIR}/lib -lpgc_protocol_cgo
#include <stdlib.h>
#include "pgc_protocol_cgo.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Err maps a non-zero C status to a Go error, preserving the codec's taxonomy.
type Err int32

func (e Err) Error() string {
	switch int32(e) {
	case C.PGC_ERR_TOO_SHORT:
		return "codec: frame too short"
	case C.PGC_ERR_UNSUPPORTED_VERSION:
		return "codec: unsupported protocol version"
	case C.PGC_ERR_UNKNOWN_TYPE:
		return "codec: unknown type code"
	case C.PGC_ERR_WRONG_DIRECTION:
		return "codec: wrong-direction type code"
	case C.PGC_ERR_TOO_LARGE:
		return "codec: frame too large"
	case C.PGC_ERR_MALFORMED:
		return "codec: malformed payload"
	case C.PGC_ERR_NULL_ARG:
		return "codec: null argument"
	case C.PGC_ERR_PANIC:
		return "codec: panic at FFI boundary"
	default:
		return fmt.Sprintf("codec: unknown error %d", int32(e))
	}
}

// reencode is the shared body: call one of the C reencode_* fns, copy the
// Rust-allocated output into Go memory, and free the Rust buffer. The returned
// slice is owned by Go. Never retains a Go pointer across the call, and frees
// the Rust buffer exactly once (the cgo pointer rule + the §4.1 ownership rule).
func reencode(
	fn func(buf *C.uint8_t, length C.size_t, out **C.uint8_t, outLen *C.size_t) C.int32_t,
	frame []byte,
) ([]byte, error) {
	var out *C.uint8_t
	var outLen C.size_t

	var bufPtr *C.uint8_t
	if len(frame) > 0 {
		// &frame[0] is pinned for the duration of this cgo call; Rust only reads.
		bufPtr = (*C.uint8_t)(unsafe.Pointer(&frame[0]))
	}

	rc := fn(bufPtr, C.size_t(len(frame)), &out, &outLen)
	if rc != C.PGC_OK {
		return nil, Err(int32(rc))
	}
	// Copy the Rust-owned buffer into Go memory, then hand it back to Rust.
	defer C.pgc_free(out, outLen)
	return C.GoBytes(unsafe.Pointer(out), C.int(outLen)), nil
}

// ReencodeClient decodes a client->server frame and re-encodes it, returning the
// canonical bytes. Equivalent to encode(decode(frame)); the byte-identity of
// this round-trip against the committed golden vectors is the Go NFR-10 proof.
func ReencodeClient(frame []byte) ([]byte, error) {
	return reencode(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_reencode_client(b, l, o, ol)
	}, frame)
}

// ReencodeServer decodes a server->client frame and re-encodes it.
func ReencodeServer(frame []byte) ([]byte, error) {
	return reencode(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_reencode_server(b, l, o, ol)
	}, frame)
}

// ReencodeEvent decodes a stored event and re-encodes it.
func ReencodeEvent(frame []byte) ([]byte, error) {
	return reencode(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_reencode_event(b, l, o, ol)
	}, frame)
}

// ProtocolVersion returns the protocol major version the linked codec speaks
// (§4A.1), so the server can assert it matches without hard-coding the byte.
func ProtocolVersion() uint8 {
	return uint8(C.pgc_protocol_version())
}
