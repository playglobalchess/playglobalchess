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
)

// This file is the Go leg of the GS-6a message seam (SPEC-S2-GAME-SERVER-GS6A).
// It gives the WS transport (GS-6) a typed `§4A client frame -> ClientMsg` read
// path and a `ServerMsg -> §4A server frame` write path, WITHOUT the Go tier
// ever hand-encoding a §4A byte (NFR-5): exactly the GS-4a event-seam shape.
//
//   DecodeClient(frame)  -> §4A client frame  --pgc_decode_client--> postcard
//                           of a ClientMsg; Go unmarshals the intermediate.
//   EncodeServer(msg)    -> Go marshals a ServerMsg to its postcard
//                           intermediate --pgc_encode_server--> §4A server frame.
//
// Only these two directions exist by design: the server RECEIVES client frames
// and SENDS server frames, so it never encodes a client frame or decodes a
// server frame. (The reencode seam already covers relaying either direction as
// opaque bytes.)
//
// The postcard writer/reader (postcard.go) is the same faithful subset proven
// byte-for-byte against the Rust encoder — here it serializes a ClientMsg /
// ServerMsg exactly as serde+postcard do: a uvarint VARIANT INDEX (declaration
// order of the enum in protocol/codec/src/message.rs) followed by the variant's
// fields in declaration order. The golden round-trip tests (message_seam_test.go)
// drive the REAL cgo seam, so a drift between this mirror and Rust fails loudly.

// --- typed Go mirrors of ClientMsg / ServerMsg ------------------------------
//
// These carry only the §4A payload (the ver|type header is the codec's job).
// Field names/order mirror protocol/codec/src/message.rs. Ids are plain strings
// here (the Rust newtypes serialize transparently as the bare string).

// ClientMsgType is the discriminant of a decoded client message.
type ClientMsgType uint8

// Client message discriminants — serde VARIANT INDEX (declaration order of the
// ClientMsg enum in message.rs), NOT the §4A.2 type code. Named so a Rust
// reorder is caught by the golden parity test with a clear mapping.
const (
	CMAuthenticate ClientMsgType = iota // 0
	CMSeek                              // 1
	CMMove                              // 2
	CMOfferDraw                         // 3
	CMResign                            // 4
	CMAbort                             // 5
	CMTakeback                          // 6
	CMRematch                           // 7
	CMPing                              // 8
	CMResync                            // 9 (§4A v1.1, ADR-17)
)

// ClientMsg is a decoded client->server message. Exactly one payload pointer is
// non-nil, selected by Type (a tagged union, matching game.Event's shape).
type ClientMsg struct {
	Type         ClientMsgType
	Authenticate *AuthenticatePayload
	Seek         *SeekPayload
	Move         *MovePayload
	// OfferDraw/Resign/Abort/Takeback/Rematch/Resync all carry only a game id.
	GameID string
	Ping   *PingPayload
}

// AuthenticatePayload — §4A.3 AUTHENTICATE: token:str.
type AuthenticatePayload struct {
	Token string
}

// SeekPayload — §4A.3 SEEK: pool_id:str, time_control:str, rated:bool.
type SeekPayload struct {
	PoolID      string
	TimeControl string
	Rated       bool
}

// MovePayload — §4A.3 MOVE: game_id:str, ply_expected:u32, uci:str.
type MovePayload struct {
	GameID      string
	PlyExpected uint32
	UCI         string
}

// PingPayload — §4A.3 PING: t:u64.
type PingPayload struct {
	T uint64
}

// ServerMsgType is the discriminant of a server message to encode.
type ServerMsgType uint8

// Frozen §4A.4 MOVE_REJECTED reason codes (mirrors game.RejectReason and the
// Rust Reason enum8). Named so callers never scatter magic bytes.
const (
	ReasonIllegal     uint8 = 0x01
	ReasonStalePly    uint8 = 0x02
	ReasonGameOver    uint8 = 0x03
	ReasonNotYourTurn uint8 = 0x04
	ReasonNotYourSeat uint8 = 0x05
	ReasonUnknownGame uint8 = 0x06
)

// Frozen §4A.7 ERROR codes (mirrors the Rust ErrorCode enum8).
const (
	ECUnsupportedVersion uint8 = 0x01
	ECProtocolViolation  uint8 = 0x02
	ECUnknownGame        uint8 = 0x03
	ECUnauthenticated    uint8 = 0x04
	ECRateLimited        uint8 = 0x05
	ECInternal           uint8 = 0x06
)

// Server message discriminants — serde VARIANT INDEX (declaration order of the
// ServerMsg enum in message.rs), NOT the §4A.2 type code.
const (
	SMMatched      ServerMsgType = iota // 0
	SMMoveAccepted                      // 1
	SMMoveRejected                      // 2
	SMState                             // 3
	SMGameEnd                           // 4
	SMPong                              // 5
	SMDrawOffered                       // 6 (§4A v1.1, ADR-17 — before Error, matching the Rust enum order)
	SMError                             // 7
)

// StateBody — §4A.6 shared state format: fen:str, turn:u8, clocks:[u32].
// Reused verbatim as Matched.StartState (one state format, Q-P4).
type StateBody struct {
	FEN    string
	Turn   uint8
	Clocks []uint32
}

// ServerMsg is a server->client message to encode. Exactly one payload pointer
// is non-nil, selected by Type.
type ServerMsg struct {
	Type         ServerMsgType
	Matched      *MatchedPayload
	MoveAccepted *MoveAcceptedPayload
	MoveRejected *MoveRejectedPayload
	State        *StatePayload
	GameEnd      *GameEndPayload
	Pong         *PongPayload
	DrawOffered  *DrawOfferedPayload
	Error        *ErrorPayload
}

// DrawOfferedPayload — §4A v1.1 (ADR-17) DRAW_OFFERED: game_id, by:u8.
type DrawOfferedPayload struct {
	GameID string
	By     uint8
}

// MatchedPayload — §4A.3 MATCHED: game_id, seat_idx:u8, opponent:str, start_state.
type MatchedPayload struct {
	GameID     string
	SeatIdx    uint8
	Opponent   string
	StartState StateBody
}

// MoveAcceptedPayload — §4A.3 MOVE_ACCEPTED (v1.2, ADR-18): game_id, ply:u32,
// uci:str5, clock_after_ms:u32. uci is the move just applied, carried so both
// seats can advance their boards (broadcast to both).
type MoveAcceptedPayload struct {
	GameID       string
	Ply          uint32
	UCI          string
	ClockAfterMs uint32
}

// MoveRejectedPayload — §4A.3 MOVE_REJECTED: game_id, ply_expected:u32, reason:enum8.
type MoveRejectedPayload struct {
	GameID      string
	PlyExpected uint32
	Reason      uint8 // §4A.4 Reason enum8 byte (ILLEGAL=0x01..UNKNOWN_GAME=0x06).
}

// StatePayload — §4A.3 STATE: game_id, state (StateBody inline).
type StatePayload struct {
	GameID string
	State  StateBody
}

// GameEndPayload — §4A.3 GAME_END: game_id, result_vector:[u8], termination:enum8,
// rating_delta:[i16].
type GameEndPayload struct {
	GameID       string
	ResultVector []uint8 // §4A.3 result bytes (0=loss,1=draw,2=win).
	Termination  uint8   // §4A.5 Termination enum8 byte (MATE=0x01..).
	RatingDelta  []int16
}

// PongPayload — §4A.3 PONG: t:u64, server_t:u64.
type PongPayload struct {
	T       uint64
	ServerT uint64
}

// ErrorPayload — §4A.3 ERROR: code:enum8, msg:str.
type ErrorPayload struct {
	Code uint8 // §4A.7 ErrorCode enum8 byte (UNSUPPORTED_VERSION=0x01..).
	Msg  string
}

// --- the seam entry points --------------------------------------------------

// DecodeClient parses a §4A client->server frame to a typed ClientMsg via the
// Rust seam (pgc_decode_client). A frame the codec rejects (bad version, wrong
// direction, malformed) returns the typed codec error; a decode of the
// intermediate the seam emits is the inverse of marshalClientIntermediate.
func DecodeClient(frame []byte) (ClientMsg, error) {
	inter, err := callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_decode_client(b, l, o, ol)
	}, frame)
	if err != nil {
		return ClientMsg{}, err
	}
	return unmarshalClientIntermediate(inter)
}

// EncodeServer serializes a ServerMsg to its canonical §4A server-frame bytes
// via the Rust seam (pgc_encode_server). The returned bytes are byte-identical
// to what any other codec consumer produces for the same message (NFR-10).
//
// Error taxonomy: a ServerMsg carrying an out-of-domain discriminant (an unknown
// reason/termination/error-code, or a result byte ∉ {0,1,2}) is REFUSED by the
// Rust encode_server (write/read symmetry — it will not emit a frame it would
// itself reject). That surfaces here as a typed codec error (MalformedPayload),
// NOT ErrBadIntermediate: the intermediate deserialized fine, the §4A build
// refused the value. ErrBadIntermediate is reserved for a genuinely corrupt
// caller-built intermediate (a mapping bug), which the Go marshaller does not
// produce for a well-formed ServerMsg.
func EncodeServer(msg ServerMsg) ([]byte, error) {
	inter, err := marshalServerIntermediate(msg)
	if err != nil {
		return nil, err
	}
	return callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_encode_server(b, l, o, ol)
	}, inter)
}

// EncodeClient serializes a ClientMsg to its canonical §4A client-frame bytes via
// the Rust seam (pgc_encode_client) — the mirror of DecodeClient. The SERVER
// never calls this (it only receives client frames); it exists for a CLIENT and
// for the transport's own end-to-end test, which acts as a client.
func EncodeClient(msg ClientMsg) ([]byte, error) {
	inter, err := marshalClientIntermediate(msg)
	if err != nil {
		return nil, err
	}
	return callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_encode_client(b, l, o, ol)
	}, inter)
}

// DecodeServer parses a §4A server-frame to a typed ServerMsg via the Rust seam
// (pgc_decode_server) — the mirror of EncodeServer. For a CLIENT / the transport
// e2e test (the server never decodes a server frame in production).
func DecodeServer(frame []byte) (ServerMsg, error) {
	inter, err := callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_decode_server(b, l, o, ol)
	}, frame)
	if err != nil {
		return ServerMsg{}, err
	}
	return unmarshalServerIntermediate(inter)
}

// --- ClientMsg intermediate (decode side; marshal provided for the drift test) ---

// marshalClientIntermediate serializes a ClientMsg to the postcard bytes serde
// produces for the ClientMsg enum: uvarint(variant index) + fields in
// declaration order. Used by the round-trip test to prove the Go mirror matches
// Rust's encode_client_intermediate byte-for-byte.
func marshalClientIntermediate(m ClientMsg) ([]byte, error) {
	w := &pcWriter{}
	w.uvarint(uint64(m.Type))
	switch m.Type {
	case CMAuthenticate:
		if m.Authenticate == nil {
			return nil, errNilPayload("Authenticate")
		}
		w.str(m.Authenticate.Token)
	case CMSeek:
		if m.Seek == nil {
			return nil, errNilPayload("Seek")
		}
		w.str(m.Seek.PoolID)
		w.str(m.Seek.TimeControl)
		w.boolean(m.Seek.Rated)
	case CMMove:
		if m.Move == nil {
			return nil, errNilPayload("Move")
		}
		w.str(m.Move.GameID)
		w.uvarint(uint64(m.Move.PlyExpected))
		w.str(m.Move.UCI)
	case CMOfferDraw, CMResign, CMAbort, CMTakeback, CMRematch, CMResync:
		w.str(m.GameID)
	case CMPing:
		if m.Ping == nil {
			return nil, errNilPayload("Ping")
		}
		w.uvarint(m.Ping.T)
	default:
		return nil, fmt.Errorf("codec: unknown client message type %d", m.Type)
	}
	return w.buf, nil
}

// unmarshalClientIntermediate is the inverse — reads the postcard of a ClientMsg.
func unmarshalClientIntermediate(b []byte) (ClientMsg, error) {
	r := &pcReader{buf: b}
	variant, err := r.uvarint()
	if err != nil {
		return ClientMsg{}, err
	}
	var m ClientMsg
	switch ClientMsgType(variant) {
	case CMAuthenticate:
		tok, err := r.str()
		if err != nil {
			return ClientMsg{}, err
		}
		m = ClientMsg{Type: CMAuthenticate, Authenticate: &AuthenticatePayload{Token: tok}}
	case CMSeek:
		pool, err := r.str()
		if err != nil {
			return ClientMsg{}, err
		}
		tc, err := r.str()
		if err != nil {
			return ClientMsg{}, err
		}
		rated, err := r.boolean()
		if err != nil {
			return ClientMsg{}, err
		}
		m = ClientMsg{Type: CMSeek, Seek: &SeekPayload{PoolID: pool, TimeControl: tc, Rated: rated}}
	case CMMove:
		gid, err := r.str()
		if err != nil {
			return ClientMsg{}, err
		}
		ply, err := r.uvarint()
		if err != nil {
			return ClientMsg{}, err
		}
		uci, err := r.str()
		if err != nil {
			return ClientMsg{}, err
		}
		m = ClientMsg{Type: CMMove, Move: &MovePayload{GameID: gid, PlyExpected: uint32(ply), UCI: uci}}
	case CMOfferDraw, CMResign, CMAbort, CMTakeback, CMRematch, CMResync:
		gid, err := r.str()
		if err != nil {
			return ClientMsg{}, err
		}
		m = ClientMsg{Type: ClientMsgType(variant), GameID: gid}
	case CMPing:
		t, err := r.uvarint()
		if err != nil {
			return ClientMsg{}, err
		}
		m = ClientMsg{Type: CMPing, Ping: &PingPayload{T: t}}
	default:
		return ClientMsg{}, fmt.Errorf("codec: unknown client message variant index %d", variant)
	}
	if r.off != len(r.buf) {
		return ClientMsg{}, fmt.Errorf("codec: %d trailing bytes in client intermediate", len(r.buf)-r.off)
	}
	return m, nil
}

// --- ServerMsg intermediate (encode side; unmarshal provided for the drift test) ---

// marshalServerIntermediate serializes a ServerMsg to the postcard bytes serde
// produces: uvarint(variant index) + fields in declaration order. Handed to
// pgc_encode_server, which turns it into the canonical §4A server frame.
func marshalServerIntermediate(m ServerMsg) ([]byte, error) {
	w := &pcWriter{}
	w.uvarint(uint64(m.Type))
	switch m.Type {
	case SMMatched:
		if m.Matched == nil {
			return nil, errNilPayload("Matched")
		}
		w.str(m.Matched.GameID)
		w.u8(m.Matched.SeatIdx)
		w.str(m.Matched.Opponent)
		w.stateBody(m.Matched.StartState)
	case SMMoveAccepted:
		if m.MoveAccepted == nil {
			return nil, errNilPayload("MoveAccepted")
		}
		w.str(m.MoveAccepted.GameID)
		w.uvarint(uint64(m.MoveAccepted.Ply))
		w.str(m.MoveAccepted.UCI)
		w.uvarint(uint64(m.MoveAccepted.ClockAfterMs))
	case SMMoveRejected:
		if m.MoveRejected == nil {
			return nil, errNilPayload("MoveRejected")
		}
		w.str(m.MoveRejected.GameID)
		w.uvarint(uint64(m.MoveRejected.PlyExpected))
		w.u8(m.MoveRejected.Reason)
	case SMState:
		if m.State == nil {
			return nil, errNilPayload("State")
		}
		w.str(m.State.GameID)
		w.stateBody(m.State.State)
	case SMGameEnd:
		if m.GameEnd == nil {
			return nil, errNilPayload("GameEnd")
		}
		w.str(m.GameEnd.GameID)
		w.uvarint(uint64(len(m.GameEnd.ResultVector)))
		for _, v := range m.GameEnd.ResultVector {
			w.u8(v)
		}
		w.u8(m.GameEnd.Termination)
		w.uvarint(uint64(len(m.GameEnd.RatingDelta)))
		for _, d := range m.GameEnd.RatingDelta {
			w.i16(d)
		}
	case SMPong:
		if m.Pong == nil {
			return nil, errNilPayload("Pong")
		}
		w.uvarint(m.Pong.T)
		w.uvarint(m.Pong.ServerT)
	case SMDrawOffered:
		if m.DrawOffered == nil {
			return nil, errNilPayload("DrawOffered")
		}
		w.str(m.DrawOffered.GameID)
		w.u8(m.DrawOffered.By)
	case SMError:
		if m.Error == nil {
			return nil, errNilPayload("Error")
		}
		w.u8(m.Error.Code)
		w.str(m.Error.Msg)
	default:
		return nil, fmt.Errorf("codec: unknown server message type %d", m.Type)
	}
	return w.buf, nil
}

// unmarshalServerIntermediate is the inverse — used only by the round-trip test
// (the production encode path never reads a server intermediate back).
func unmarshalServerIntermediate(b []byte) (ServerMsg, error) {
	r := &pcReader{buf: b}
	variant, err := r.uvarint()
	if err != nil {
		return ServerMsg{}, err
	}
	var m ServerMsg
	switch ServerMsgType(variant) {
	case SMMatched:
		gid, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		seat, err := r.u8()
		if err != nil {
			return ServerMsg{}, err
		}
		opp, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		st, err := r.stateBody()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMMatched, Matched: &MatchedPayload{GameID: gid, SeatIdx: seat, Opponent: opp, StartState: st}}
	case SMMoveAccepted:
		gid, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		ply, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		uci, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		clk, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMMoveAccepted, MoveAccepted: &MoveAcceptedPayload{GameID: gid, Ply: uint32(ply), UCI: uci, ClockAfterMs: uint32(clk)}}
	case SMMoveRejected:
		gid, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		ply, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		reason, err := r.u8()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMMoveRejected, MoveRejected: &MoveRejectedPayload{GameID: gid, PlyExpected: uint32(ply), Reason: reason}}
	case SMState:
		gid, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		st, err := r.stateBody()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMState, State: &StatePayload{GameID: gid, State: st}}
	case SMGameEnd:
		gid, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		n, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		rv := make([]uint8, 0, n)
		for i := uint64(0); i < n; i++ {
			v, err := r.u8()
			if err != nil {
				return ServerMsg{}, err
			}
			rv = append(rv, v)
		}
		term, err := r.u8()
		if err != nil {
			return ServerMsg{}, err
		}
		m2, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		rd := make([]int16, 0, m2)
		for i := uint64(0); i < m2; i++ {
			d, err := r.i16()
			if err != nil {
				return ServerMsg{}, err
			}
			rd = append(rd, d)
		}
		m = ServerMsg{Type: SMGameEnd, GameEnd: &GameEndPayload{GameID: gid, ResultVector: rv, Termination: term, RatingDelta: rd}}
	case SMPong:
		t, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		st, err := r.uvarint()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMPong, Pong: &PongPayload{T: t, ServerT: st}}
	case SMDrawOffered:
		gid, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		by, err := r.u8()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMDrawOffered, DrawOffered: &DrawOfferedPayload{GameID: gid, By: by}}
	case SMError:
		code, err := r.u8()
		if err != nil {
			return ServerMsg{}, err
		}
		msg, err := r.str()
		if err != nil {
			return ServerMsg{}, err
		}
		m = ServerMsg{Type: SMError, Error: &ErrorPayload{Code: code, Msg: msg}}
	default:
		return ServerMsg{}, fmt.Errorf("codec: unknown server message variant index %d", variant)
	}
	if r.off != len(r.buf) {
		return ServerMsg{}, fmt.Errorf("codec: %d trailing bytes in server intermediate", len(r.buf)-r.off)
	}
	return m, nil
}

// stateBody serializes a StateBody inline (postcard nests with no framing):
// fen:str, turn:u8, clocks:Vec<u32>. Matches types.rs StateBody field order.
func (w *pcWriter) stateBody(s StateBody) {
	w.str(s.FEN)
	w.u8(s.Turn)
	w.uvarint(uint64(len(s.Clocks)))
	for _, c := range s.Clocks {
		w.uvarint(uint64(c))
	}
}

func (r *pcReader) stateBody() (StateBody, error) {
	fen, err := r.str()
	if err != nil {
		return StateBody{}, err
	}
	turn, err := r.u8()
	if err != nil {
		return StateBody{}, err
	}
	n, err := r.uvarint()
	if err != nil {
		return StateBody{}, err
	}
	clocks := make([]uint32, 0, n)
	for i := uint64(0); i < n; i++ {
		c, err := r.uvarint()
		if err != nil {
			return StateBody{}, err
		}
		clocks = append(clocks, uint32(c))
	}
	return StateBody{FEN: fen, Turn: turn, Clocks: clocks}, nil
}

func errNilPayload(name string) error {
	return fmt.Errorf("codec: %s message has nil payload", name)
}

// --- test-support seam pokes (exported to the package's _test.go, which cannot
// hold its own cgo preamble) ------------------------------------------------

// rawClientIntermediate returns the postcard intermediate the Rust seam emits
// for a §4A client frame — the Rust-produced oracle the parity test compares the
// Go marshaller against. Test-only (used by message_seam_test.go).
func rawClientIntermediate(frame []byte) ([]byte, error) {
	return callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_decode_client(b, l, o, ol)
	}, frame)
}

// rawEncodeServerIntermediate feeds raw bytes straight to pgc_encode_server,
// bypassing the Go marshaller — so a test can assert a corrupt intermediate maps
// to ErrBadIntermediate. Test-only.
func rawEncodeServerIntermediate(inter []byte) ([]byte, error) {
	return callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_encode_server(b, l, o, ol)
	}, inter)
}
