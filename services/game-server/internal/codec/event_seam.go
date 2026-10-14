package codec

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../protocol/codec-cgo/include
#cgo LDFLAGS: -L${SRCDIR}/lib -lpgc_protocol_cgo
#include <stdlib.h>
#include "pgc_protocol_cgo.h"
*/
import "C"

import (
	"errors"
	"fmt"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// This file is the Go leg of the GS-4a event seam (SPEC-S2-GAME-SERVER-GS4A). It
// gives the durable store (GS-4) a `game.Event ⇄ §4A event-frame bytes` path,
// WITHOUT the Go tier ever hand-encoding a §4A byte (NFR-5): Go only builds the
// in-process "intermediate" (postcard of an EventEnvelope mirror) and hands it to
// the Rust seam (pgc_encode_event), which does all §4A work. The reverse decodes
// a §4A frame to the intermediate and we read it back.
//
// The postcard writer/reader below is a FAITHFUL re-implementation of the subset
// of the postcard format the EventEnvelope uses — proven byte-for-byte against
// vectors emitted by the real Rust encoder (event_seam_test.go golden fixtures),
// never hand-trusted. It is deliberately tiny and closed to exactly this one
// struct shape; it is NOT a general postcard library and must not grow into one.

// ErrBadIntermediate is returned when the Rust seam rejects the intermediate we
// built (PGC_ERR_BAD_INTERMEDIATE) — a bug in OUR mapping/encoding, distinct from
// a corrupt stored §4A frame.
var ErrBadIntermediate = errors.New("codec: seam rejected the event intermediate (bad mapping)")

// EventHeader carries the §4A.8 envelope fields that live alongside the body: the
// store (GS-4) owns stream_id + seq, and supplies schema_ver + ts.
type EventHeader struct {
	StreamID  string
	Seq       uint32
	SchemaVer uint16
	TS        uint64
}

// EncodeEvent serializes (header + event) to its canonical §4A event-frame bytes
// via the Rust seam. The returned bytes are byte-identical to what the wire path
// produces for the same event (NFR-10) — that is the store's durable form.
func EncodeEvent(h EventHeader, ev game.Event) ([]byte, error) {
	inter, err := marshalIntermediate(h, ev)
	if err != nil {
		return nil, err
	}
	out, err := callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_encode_event(b, l, o, ol)
	}, inter)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DecodeEvent parses a stored §4A event frame back to (header, event) via the
// Rust seam. The inverse of EncodeEvent.
func DecodeEvent(frame []byte) (EventHeader, game.Event, error) {
	inter, err := callSeam(func(b *C.uint8_t, l C.size_t, o **C.uint8_t, ol *C.size_t) C.int32_t {
		return C.pgc_decode_event(b, l, o, ol)
	}, frame)
	if err != nil {
		return EventHeader{}, game.Event{}, err
	}
	return unmarshalIntermediate(inter)
}

// callSeam runs one seam fn with the same copy-out+free ownership as reencode,
// mapping PGC_ERR_BAD_INTERMEDIATE to the typed Go error.
func callSeam(
	fn func(buf *C.uint8_t, length C.size_t, out **C.uint8_t, outLen *C.size_t) C.int32_t,
	in []byte,
) ([]byte, error) {
	out, err := reencode(fn, in)
	if err != nil {
		var ce Err
		if errors.As(err, &ce) && int32(ce) == C.PGC_ERR_BAD_INTERMEDIATE {
			return nil, ErrBadIntermediate
		}
		return nil, err
	}
	return out, nil
}

// --- the EventEnvelope mirror: game.Event <-> intermediate ------------------

// marshalIntermediate maps (header, game.Event) to the postcard bytes of an
// EventEnvelope: stream_id, seq, schema_ver, ts, event (variant-index + body).
// Field order MUST match protocol/codec/src/event.rs EventEnvelope + GameEvent.
func marshalIntermediate(h EventHeader, ev game.Event) ([]byte, error) {
	w := &pcWriter{}
	w.str(h.StreamID)
	w.uvarint(uint64(h.Seq))
	w.uvarint(uint64(h.SchemaVer))
	w.uvarint(h.TS)
	if err := w.gameEvent(ev); err != nil {
		return nil, err
	}
	return w.buf, nil
}

// unmarshalIntermediate is the inverse.
func unmarshalIntermediate(b []byte) (EventHeader, game.Event, error) {
	r := &pcReader{buf: b}
	h := EventHeader{}
	var err error
	if h.StreamID, err = r.str(); err != nil {
		return h, game.Event{}, err
	}
	var seq, schemaVer, ts uint64
	if seq, err = r.uvarint(); err != nil {
		return h, game.Event{}, err
	}
	if schemaVer, err = r.uvarint(); err != nil {
		return h, game.Event{}, err
	}
	if ts, err = r.uvarint(); err != nil {
		return h, game.Event{}, err
	}
	// Narrowing from uvarint's u64 is safe on the DECODE path: the only producer
	// of this intermediate is Rust's encode_envelope_intermediate, whose
	// EventEnvelope already types seq as u32 and schema_ver as u16, so an
	// over-wide value cannot occur here (N2).
	h.Seq = uint32(seq)
	h.SchemaVer = uint16(schemaVer)
	h.TS = ts
	ev, err := r.gameEvent()
	if err != nil {
		return h, game.Event{}, err
	}
	if r.off != len(r.buf) {
		return h, game.Event{}, fmt.Errorf("codec: %d trailing bytes in intermediate", len(r.buf)-r.off)
	}
	return h, ev, nil
}

// gameEvent variant indices — serde derives these from DECLARATION ORDER of the
// GameEvent enum in event.rs. Keeping them as named constants (not magic numbers)
// so a reorder in Rust is caught by the golden parity test with a clear mapping.
const (
	vGameCreated = iota // 0
	vPlayerJoined
	vMovePlayed
	vDrawOffered
	vDrawAccepted
	vResigned
	vAborted
	vTakebackReq
	vTakebackAccepted
	vClockFlagged
	vGameEnded
	vRatingApplied
)

func (w *pcWriter) gameEvent(ev game.Event) error {
	switch ev.Type {
	case game.EvGameCreated:
		b := ev.GameCreated
		if b == nil {
			return errNilBody("GameCreated")
		}
		w.uvarint(vGameCreated)
		w.str(b.PoolID)
		w.boolean(b.Rated)
		w.uvarint(uint64(len(b.Seats)))
		for _, s := range b.Seats {
			w.u8(s.SeatIdx)
			w.u8(s.Color)
		}
		w.str(b.StartFEN)
		w.str(b.TimeControl)
	case game.EvPlayerJoined:
		b := ev.PlayerJoined
		if b == nil {
			return errNilBody("PlayerJoined")
		}
		w.uvarint(vPlayerJoined)
		w.u8(b.SeatIdx)
		w.str(b.UserID)
	case game.EvMovePlayed:
		b := ev.MovePlayed
		if b == nil {
			return errNilBody("MovePlayed")
		}
		w.uvarint(vMovePlayed)
		w.uvarint(uint64(b.Ply))
		w.u8(b.SeatIdx)
		w.str(b.UCI)
		w.uvarint(uint64(b.ClockAfterMs))
		w.uvarint(b.ServerTS)
	case game.EvDrawOffered:
		if err := w.seatIdxBody(ev.SeatAction, vDrawOffered, "DrawOffered"); err != nil {
			return err
		}
	case game.EvDrawAccepted:
		if err := w.seatIdxBody(ev.SeatAction, vDrawAccepted, "DrawAccepted"); err != nil {
			return err
		}
	case game.EvResigned:
		if err := w.seatIdxBody(ev.SeatAction, vResigned, "Resigned"); err != nil {
			return err
		}
	case game.EvTakebackReq:
		if err := w.seatIdxBody(ev.SeatAction, vTakebackReq, "TakebackReq"); err != nil {
			return err
		}
	case game.EvTakebackAccepted:
		if err := w.seatIdxBody(ev.SeatAction, vTakebackAccepted, "TakebackAccepted"); err != nil {
			return err
		}
	case game.EvClockFlagged:
		if err := w.seatIdxBody(ev.SeatAction, vClockFlagged, "ClockFlagged"); err != nil {
			return err
		}
	case game.EvAborted:
		b := ev.Aborted
		if b == nil {
			return errNilBody("Aborted")
		}
		w.uvarint(vAborted)
		w.u8(b.By)
	case game.EvGameEnded:
		b := ev.GameEnded
		if b == nil {
			return errNilBody("GameEnded")
		}
		w.uvarint(vGameEnded)
		w.uvarint(uint64(len(b.ResultVector)))
		for _, r := range b.ResultVector {
			w.u8(uint8(r))
		}
		w.u8(uint8(b.Termination))
	case game.EvRatingApplied:
		b := ev.RatingApplied
		if b == nil {
			return errNilBody("RatingApplied")
		}
		w.uvarint(vRatingApplied)
		w.str(b.PoolID)
		w.uvarint(uint64(len(b.PerSeat)))
		for _, s := range b.PerSeat {
			w.u8(s.SeatIdx)
			w.i16(s.Before)
			w.i16(s.After)
			w.uvarint(uint64(s.RD))
			w.f32(s.Vol)
		}
	default:
		return fmt.Errorf("codec: unknown event type %d", ev.Type)
	}
	return nil
}

func (w *pcWriter) seatIdxBody(b *game.SeatAction, variant int, name string) error {
	if b == nil {
		return errNilBody(name)
	}
	w.uvarint(uint64(variant))
	w.u8(b.SeatIdx)
	return nil
}

func (r *pcReader) gameEvent() (game.Event, error) {
	variant, err := r.uvarint()
	if err != nil {
		return game.Event{}, err
	}
	switch variant {
	case vGameCreated:
		poolID, err := r.str()
		if err != nil {
			return game.Event{}, err
		}
		rated, err := r.boolean()
		if err != nil {
			return game.Event{}, err
		}
		n, err := r.uvarint()
		if err != nil {
			return game.Event{}, err
		}
		seats := make([]game.Seat, 0, n)
		for i := uint64(0); i < n; i++ {
			si, err := r.u8()
			if err != nil {
				return game.Event{}, err
			}
			col, err := r.u8()
			if err != nil {
				return game.Event{}, err
			}
			seats = append(seats, game.Seat{SeatIdx: si, Color: col})
		}
		startFEN, err := r.str()
		if err != nil {
			return game.Event{}, err
		}
		tc, err := r.str()
		if err != nil {
			return game.Event{}, err
		}
		return game.Event{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: poolID, Rated: rated, Seats: seats, StartFEN: startFEN, TimeControl: tc,
		}}, nil
	case vPlayerJoined:
		si, err := r.u8()
		if err != nil {
			return game.Event{}, err
		}
		uid, err := r.str()
		if err != nil {
			return game.Event{}, err
		}
		return game.Event{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: si, UserID: uid}}, nil
	case vMovePlayed:
		ply, err := r.uvarint()
		if err != nil {
			return game.Event{}, err
		}
		si, err := r.u8()
		if err != nil {
			return game.Event{}, err
		}
		uci, err := r.str()
		if err != nil {
			return game.Event{}, err
		}
		clk, err := r.uvarint()
		if err != nil {
			return game.Event{}, err
		}
		sts, err := r.uvarint()
		if err != nil {
			return game.Event{}, err
		}
		return game.Event{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{
			Ply: uint32(ply), SeatIdx: si, UCI: uci, ClockAfterMs: uint32(clk), ServerTS: sts,
		}}, nil
	case vDrawOffered:
		return r.seatIdxEvent(game.EvDrawOffered)
	case vDrawAccepted:
		return r.seatIdxEvent(game.EvDrawAccepted)
	case vResigned:
		return r.seatIdxEvent(game.EvResigned)
	case vTakebackReq:
		return r.seatIdxEvent(game.EvTakebackReq)
	case vTakebackAccepted:
		return r.seatIdxEvent(game.EvTakebackAccepted)
	case vClockFlagged:
		return r.seatIdxEvent(game.EvClockFlagged)
	case vAborted:
		by, err := r.u8()
		if err != nil {
			return game.Event{}, err
		}
		return game.Event{Type: game.EvAborted, Aborted: &game.Aborted{By: by}}, nil
	case vGameEnded:
		n, err := r.uvarint()
		if err != nil {
			return game.Event{}, err
		}
		rv := make([]game.Result, 0, n)
		for i := uint64(0); i < n; i++ {
			v, err := r.u8()
			if err != nil {
				return game.Event{}, err
			}
			rv = append(rv, game.Result(v))
		}
		term, err := r.u8()
		if err != nil {
			return game.Event{}, err
		}
		return game.Event{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: rv, Termination: game.Termination(term),
		}}, nil
	case vRatingApplied:
		poolID, err := r.str()
		if err != nil {
			return game.Event{}, err
		}
		n, err := r.uvarint()
		if err != nil {
			return game.Event{}, err
		}
		per := make([]game.SeatRating, 0, n)
		for i := uint64(0); i < n; i++ {
			si, err := r.u8()
			if err != nil {
				return game.Event{}, err
			}
			before, err := r.i16()
			if err != nil {
				return game.Event{}, err
			}
			after, err := r.i16()
			if err != nil {
				return game.Event{}, err
			}
			rd, err := r.uvarint()
			if err != nil {
				return game.Event{}, err
			}
			vol, err := r.f32()
			if err != nil {
				return game.Event{}, err
			}
			per = append(per, game.SeatRating{SeatIdx: si, Before: before, After: after, RD: uint16(rd), Vol: vol})
		}
		return game.Event{Type: game.EvRatingApplied, RatingApplied: &game.RatingApplied{PoolID: poolID, PerSeat: per}}, nil
	default:
		return game.Event{}, fmt.Errorf("codec: unknown event variant index %d", variant)
	}
}

func (r *pcReader) seatIdxEvent(t game.EventType) (game.Event, error) {
	si, err := r.u8()
	if err != nil {
		return game.Event{}, err
	}
	return game.Event{Type: t, SeatAction: &game.SeatAction{SeatIdx: si}}, nil
}

func errNilBody(name string) error {
	return fmt.Errorf("codec: %s event has nil body", name)
}
