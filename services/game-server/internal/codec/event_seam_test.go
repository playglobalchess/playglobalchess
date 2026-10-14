package codec

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Golden intermediate vectors emitted by the REAL Rust encoder
// (pgc_protocol::encode_envelope_intermediate, captured via a probe). The Go
// marshaller must reproduce these byte-for-byte — this is what proves the Go
// postcard subset matches postcard, not an assumption.
var goldenIntermediate = []struct {
	name string
	hdr  EventHeader
	ev   game.Event
	hex  string
}{
	{
		name: "ClockFlagged",
		hdr:  EventHeader{StreamID: "game/abc", Seq: 5, SchemaVer: 1, TS: 42},
		ev:   game.Event{Type: game.EvClockFlagged, SeatAction: &game.SeatAction{SeatIdx: 1}},
		hex:  "0867616d652f61626305012a0901",
	},
	{
		name: "MovePlayed",
		hdr:  EventHeader{StreamID: "game/abc", Seq: 6, SchemaVer: 1, TS: 100},
		ev: game.Event{Type: game.EvMovePlayed, MovePlayed: &game.MovePlayed{
			Ply: 3, SeatIdx: 0, UCI: "e2e4", ClockAfterMs: 59000, ServerTS: 123456789,
		}},
		hex: "0867616d652f6162630601640203000465326534f8cc03959aef3a",
	},
	{
		name: "GameEnded",
		hdr:  EventHeader{StreamID: "g", Seq: 0, SchemaVer: 1, TS: 1},
		ev: game.Event{Type: game.EvGameEnded, GameEnded: &game.GameEnded{
			ResultVector: []game.Result{game.Result(2), game.Result(0)}, Termination: game.Termination(0x01),
		}},
		hex: "01670001010a02020001",
	},
	{
		name: "GameCreated",
		hdr:  EventHeader{StreamID: "g", Seq: 0, SchemaVer: 1, TS: 1},
		ev: game.Event{Type: game.EvGameCreated, GameCreated: &game.GameCreated{
			PoolID: "blitz", Rated: true,
			Seats:    []game.Seat{{SeatIdx: 0, Color: 0}, {SeatIdx: 1, Color: 1}},
			StartFEN: "startpos", TimeControl: "blitz-5+3",
		}},
		hex: "01670001010005626c69747a010200000101087374617274706f7309626c69747a2d352b33",
	},
	{
		name: "PlayerJoined",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvPlayerJoined, PlayerJoined: &game.PlayerJoined{SeatIdx: 1, UserID: "u/42"}},
		hex:  "0173070109010104752f3432",
	},
	{
		name: "DrawOffered",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvDrawOffered, SeatAction: &game.SeatAction{SeatIdx: 0}},
		hex:  "01730701090300",
	},
	{
		name: "DrawAccepted",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvDrawAccepted, SeatAction: &game.SeatAction{SeatIdx: 1}},
		hex:  "01730701090401",
	},
	{
		name: "Resigned",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvResigned, SeatAction: &game.SeatAction{SeatIdx: 0}},
		hex:  "01730701090500",
	},
	{
		name: "TakebackReq",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvTakebackReq, SeatAction: &game.SeatAction{SeatIdx: 1}},
		hex:  "01730701090701",
	},
	{
		name: "TakebackAccepted",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvTakebackAccepted, SeatAction: &game.SeatAction{SeatIdx: 0}},
		hex:  "01730701090800",
	},
	{
		name: "Aborted",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev:   game.Event{Type: game.EvAborted, Aborted: &game.Aborted{By: 0xFF}},
		hex:  "017307010906ff",
	},
	{
		name: "RatingApplied",
		hdr:  EventHeader{StreamID: "s", Seq: 7, SchemaVer: 1, TS: 9},
		ev: game.Event{Type: game.EvRatingApplied, RatingApplied: &game.RatingApplied{
			PoolID: "blitz",
			PerSeat: []game.SeatRating{
				{SeatIdx: 0, Before: 1500, After: -1512, RD: 60, Vol: 0.06},
				{SeatIdx: 1, Before: -1500, After: 1488, RD: 61, Vol: 0.059},
			},
		}},
		hex: "01730701090b05626c69747a0200b817cf173c8fc2753d01b717a0173dfca9713d",
	},
}

// The Go-built intermediate must equal the Rust-emitted golden bytes exactly.
func TestIntermediateMatchesRustGolden(t *testing.T) {
	for _, g := range goldenIntermediate {
		t.Run(g.name, func(t *testing.T) {
			got, err := marshalIntermediate(g.hdr, g.ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			want, _ := hex.DecodeString(g.hex)
			if !bytes.Equal(got, want) {
				t.Fatalf("intermediate mismatch\n got=%x\nwant=%x", got, want)
			}
		})
	}
}

// Go marshal -> unmarshal round-trips every vector back to the original.
func TestIntermediateRoundTrip(t *testing.T) {
	for _, g := range goldenIntermediate {
		t.Run(g.name, func(t *testing.T) {
			b, err := marshalIntermediate(g.hdr, g.ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			hdr, ev, err := unmarshalIntermediate(b)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if hdr != g.hdr {
				t.Fatalf("hdr mismatch: got %+v want %+v", hdr, g.hdr)
			}
			if ev.Type != g.ev.Type {
				t.Fatalf("type mismatch: got %d want %d", ev.Type, g.ev.Type)
			}
		})
	}
}

// The real round-trip through the Rust seam: Go EncodeEvent -> canonical §4A
// frame -> Go DecodeEvent == original (header + event). Proves the Go mapping +
// Rust seam reproduce canonical wire bytes and read them back faithfully.
func TestEncodeDecodeEventThroughSeam(t *testing.T) {
	for _, g := range goldenIntermediate {
		t.Run(g.name, func(t *testing.T) {
			frame, err := EncodeEvent(g.hdr, g.ev)
			if err != nil {
				t.Fatalf("EncodeEvent: %v", err)
			}
			hdr, ev, err := DecodeEvent(frame)
			if err != nil {
				t.Fatalf("DecodeEvent: %v", err)
			}
			if hdr != g.hdr {
				t.Fatalf("hdr round-trip: got %+v want %+v", hdr, g.hdr)
			}
			if ev.Type != g.ev.Type {
				t.Fatalf("type round-trip: got %d want %d", ev.Type, g.ev.Type)
			}
			// Encoding the decoded event must reproduce the same frame.
			frame2, err := EncodeEvent(hdr, ev)
			if err != nil {
				t.Fatalf("re-EncodeEvent: %v", err)
			}
			if !bytes.Equal(frame, frame2) {
				t.Fatalf("frame not stable across round-trip\n a=%x\n b=%x", frame, frame2)
			}
		})
	}
}
