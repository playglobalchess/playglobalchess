package codec

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// GS-6a message-seam tests. As with the GS-4a event seam, correctness is proven
// against the REAL Rust codec (never hand-reasoned bytes), via the shared,
// Rust-generated golden corpus (protocol/vectors/vectors.json — regenerated only
// by `cargo run --example gen_vectors`, guarded by the Rust golden_freeze test).
//
// The two seam directions are proven by the two paths the production code uses:
//
//   DecodeClient (server RECEIVES client frames): for every committed §4A client
//   frame, DecodeClient(frame) -> Go ClientMsg, and the Go-marshalled
//   intermediate must byte-match the intermediate the real seam
//   (pgc_decode_client) produces from the same frame. That proves the Go
//   postcard mirror == Rust's encode_client_intermediate, with the seam's own
//   output as the oracle (no stale fixture).
//
//   EncodeServer (server SENDS server frames): a Go ServerMsg built to match the
//   corpus value, run through EncodeServer, must reproduce the committed §4A
//   server frame byte-for-byte. The committed frame is the drift-proof oracle:
//   if the Go mirror drifts, the produced frame won't match. (There is no
//   frame->server-intermediate seam fn — the server never decodes a server
//   frame in production — so the server direction is proven encode-forward
//   against the frozen frame, which is the guarantee that matters: "what Go
//   sends is byte-identical to the frozen §4A wire form".)

const messageVectorsRelPath = "../../../../protocol/vectors/vectors.json"

func loadSharedVectors(t *testing.T) []sharedVector {
	t.Helper()
	path := filepath.Clean(messageVectorsRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared corpus %s: %v (regenerate via `cargo run --example gen_vectors`)", path, err)
	}
	var f sharedVectorsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f.Vectors
}

// TestClientSeamFromSharedCorpus: for every committed §4A CLIENT frame, the real
// seam decodes it to a Go ClientMsg, and the Go-marshalled intermediate must
// byte-match the intermediate the seam itself produced from the same frame.
func TestClientSeamFromSharedCorpus(t *testing.T) {
	clients := 0
	for _, v := range loadSharedVectors(t) {
		if v.Kind != "client" {
			continue
		}
		clients++
		t.Run(v.Name, func(t *testing.T) {
			frame, err := hex.DecodeString(v.Hex)
			if err != nil {
				t.Fatalf("bad hex in corpus: %v", err)
			}
			// Rust's intermediate for this frame (the oracle).
			rustInter, err := rawDecodeClientIntermediate(frame)
			if err != nil {
				t.Fatalf("seam decode %s: %v", v.Name, err)
			}
			// Go unmarshal -> re-marshal must equal the oracle byte-for-byte.
			msg, err := unmarshalClientIntermediate(rustInter)
			if err != nil {
				t.Fatalf("unmarshal client intermediate %s: %v — Go mirror may have drifted", v.Name, err)
			}
			goInter, err := marshalClientIntermediate(msg)
			if err != nil {
				t.Fatalf("marshal client intermediate %s: %v", v.Name, err)
			}
			if !bytes.Equal(goInter, rustInter) {
				t.Fatalf("%s: Go client intermediate != Rust\n go  =%x\n rust=%x", v.Name, goInter, rustInter)
			}
			// The public DecodeClient yields the same message.
			msg2, err := DecodeClient(frame)
			if err != nil {
				t.Fatalf("DecodeClient %s: %v", v.Name, err)
			}
			reGo, err := marshalClientIntermediate(msg2)
			if err != nil {
				t.Fatalf("re-marshal %s: %v", v.Name, err)
			}
			if !bytes.Equal(reGo, rustInter) {
				t.Fatalf("%s: DecodeClient round-trip drifted", v.Name)
			}
		})
	}
	if clients == 0 {
		t.Fatalf("shared corpus carried no kind=client vectors — guard vacuous")
	}
	if clients < 9 { // 9 §4A.2 C codes
		t.Fatalf("expected >=9 client vectors, got %d", clients)
	}
}

// TestServerSeamFromSharedCorpus: a Go ServerMsg built to match each committed
// corpus value, run through EncodeServer, must reproduce the committed §4A
// server frame byte-for-byte. corpusServerMsgs mirrors the §4A server vectors in
// protocol/codec/tests/corpus/mod.rs; a mismatch means either the Go mirror or
// this fixture drifted from the frozen corpus (both are caught — a frozen frame
// cannot be reproduced by a wrong message or a wrong marshaller).
func TestServerSeamFromSharedCorpus(t *testing.T) {
	byName := map[string]string{} // corpus name -> hex
	servers := 0
	for _, v := range loadSharedVectors(t) {
		if v.Kind == "server" {
			byName[v.Name] = v.Hex
			servers++
		}
	}
	if servers < 8 { // 8 §4A.2 S codes (incl v1.1 DRAW_OFFERED)
		t.Fatalf("expected >=7 server vectors, got %d", servers)
	}

	for name, msg := range corpusServerMsgs() {
		wantHex, ok := byName[name]
		if !ok {
			t.Fatalf("corpus has no server vector %q — fixture names drifted from corpus/mod.rs", name)
		}
		t.Run(name, func(t *testing.T) {
			got, err := EncodeServer(msg)
			if err != nil {
				t.Fatalf("EncodeServer %s: %v", name, err)
			}
			if hex.EncodeToString(got) != wantHex {
				t.Fatalf("%s: EncodeServer did not reproduce committed §4A frame — Go mirror or fixture drifted.\n committed=%s\n got      =%s", name, wantHex, hex.EncodeToString(got))
			}
			// Intermediate round-trips cleanly in Go too.
			inter, err := marshalServerIntermediate(msg)
			if err != nil {
				t.Fatalf("marshal %s: %v", name, err)
			}
			back, err := unmarshalServerIntermediate(inter)
			if err != nil {
				t.Fatalf("unmarshal %s: %v", name, err)
			}
			reframed, err := EncodeServer(back)
			if err != nil {
				t.Fatalf("re-encode %s: %v", name, err)
			}
			if !bytes.Equal(got, reframed) {
				t.Fatalf("%s: server frame not stable across intermediate round-trip", name)
			}
		})
	}

	// Every §4A server code must be exercised by a fixture (coverage guard).
	if len(corpusServerMsgs()) < 8 {
		t.Fatalf("corpusServerMsgs covers %d messages, expected >=8 (one per S code incl v1.1)", len(corpusServerMsgs()))
	}
}

// corpusServerMsgs mirrors the server vectors in protocol/codec/tests/corpus/mod.rs
// by NAME. These are the Go-side ServerMsg values the transport will build; the
// test proves EncodeServer turns each into the exact frozen §4A frame.
func corpusServerMsgs() map[string]ServerMsg {
	gid := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	return map[string]ServerMsg{
		"server/matched": {Type: SMMatched, Matched: &MatchedPayload{
			GameID: gid, SeatIdx: 0, Opponent: "grandmaster_jane",
			StartState: StateBody{
				FEN:  "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
				Turn: 0, Clocks: []uint32{300000, 300000},
			},
		}},
		"server/move_accepted": {Type: SMMoveAccepted, MoveAccepted: &MoveAcceptedPayload{
			GameID: gid, Ply: 18, UCI: "e2e4", ClockAfterMs: 297500,
		}},
		"server/move_rejected": {Type: SMMoveRejected, MoveRejected: &MoveRejectedPayload{
			GameID: gid, PlyExpected: 18, Reason: 0x02, // STALE_PLY
		}},
		"server/state_max_fen": {Type: SMState, State: &StatePayload{
			GameID: gid,
			State: StateBody{
				FEN:  "rnbqkbnr/pp1ppppp/8/2p5/4P3/5N2/PPPP1PPP/RNBQKB1R b KQkq e3 100 199",
				Turn: 0, Clocks: []uint32{1000, 2000},
			},
		}},
		"server/state": {Type: SMState, State: &StatePayload{
			GameID: gid,
			State: StateBody{
				FEN:  "rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
				Turn: 1, Clocks: []uint32{300000, 300000},
			},
		}},
		"server/game_end": {Type: SMGameEnd, GameEnd: &GameEndPayload{
			GameID: gid, ResultVector: []uint8{2, 0}, // WIN, LOSS
			Termination: 0x0A, RatingDelta: []int16{12, -12}, // TIMEOUT_VS_INSUFFICIENT
		}},
		"server/pong": {Type: SMPong, Pong: &PongPayload{
			T: 1728000000123, ServerT: 1728000000456,
		}},
		"server/draw_offered": {Type: SMDrawOffered, DrawOffered: &DrawOfferedPayload{
			GameID: gid, By: 1, // §4A v1.1 (ADR-17)
		}},
		"server/error": {Type: SMError, Error: &ErrorPayload{
			Code: 0x05, Msg: "slow down", // RATE_LIMITED
		}},
	}
}

// TestServerSeamRejectsBadIntermediate: a nil payload is our own mapping bug and
// is caught before the seam; a corrupt intermediate handed to pgc_encode_server
// returns the distinct ErrBadIntermediate.
func TestServerSeamRejectsBadIntermediate(t *testing.T) {
	if _, err := EncodeServer(ServerMsg{Type: SMMatched}); err == nil {
		t.Fatal("expected error for nil Matched payload")
	}
	_, err := rawEncodeServerIntermediate([]byte{0xff, 0xff, 0xff})
	if err != ErrBadIntermediate {
		t.Fatalf("expected ErrBadIntermediate, got %v", err)
	}
}

// TestClientSeamRejectsBadFrame: a malformed / wrong-direction client frame is
// rejected with a typed codec error (NOT ErrBadIntermediate — that is reserved
// for a bad caller-built intermediate on the encode side).
func TestClientSeamRejectsBadFrame(t *testing.T) {
	if _, err := DecodeClient([]byte{}); err == nil {
		t.Fatal("expected error for empty client frame")
	}
	// 0x01,0x81 = a SERVER type code in a client frame (wrong direction).
	if _, err := DecodeClient([]byte{0x01, 0x81}); err == nil {
		t.Fatal("expected wrong-direction error")
	}
}

// rawDecodeClientIntermediate returns the postcard intermediate the seam emits
// for a §4A client frame (the Rust-produced oracle bytes).
func rawDecodeClientIntermediate(frame []byte) ([]byte, error) {
	return rawClientIntermediate(frame)
}

// TestServerSeamHighU8RoundTrip proves the message path treats a u8 field as a
// single RAW byte (not a LEB128 varint) for a value >= 0x80 — the exact class of
// bug GS-4a hit (u8-as-varint, invisible for 0..127, wrong for 128..255). The
// §4A corpus has no server u8 field >= 0x80 (seat_idx/turn are 0/1), so this
// edge is otherwise untested on the message seam. seat_idx and turn carry no
// closed domain, so a high value is legal to encode; it must survive
// EncodeServer -> §4A frame -> Go unmarshal of Rust's reproduced intermediate.
func TestServerSeamHighU8RoundTrip(t *testing.T) {
	msg := ServerMsg{Type: SMMatched, Matched: &MatchedPayload{
		GameID: "f47ac10b-58cc-4372-a567-0e02b2c3d479", SeatIdx: 200,
		Opponent: "x",
		StartState: StateBody{
			FEN: "startpos", Turn: 200, Clocks: []uint32{300000, 300000},
		},
	}}
	// Prove the u8 encoding positionally against the Rust oracle: the Go
	// intermediate must byte-match pgc_encode_server's own input shape. The
	// strongest check is simply that EncodeServer accepts it and the frame is
	// STABLE across a full round-trip — but to pin the u8-vs-varint boundary
	// specifically, assert the Go intermediate places seat_idx (200) as a single
	// 0xC8 right after the game id, not as the two-byte varint 0xC8 0x01.
	inter, err := marshalServerIntermediate(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// intermediate layout: variant(1) | gid(len+36) | seat_idx(u8) | ...
	// variant byte + str-len byte + 36 gid bytes = offset 38 is seat_idx. It
	// must be the single raw byte 0xC8 (200); a varint would be 0xC8 0x01, but
	// more decisively: the NEXT field (opponent) begins right after this ONE
	// byte, so we verify by decoding — if seat_idx had consumed two bytes the
	// opponent string length/content would be wrong and unmarshal would corrupt.
	const seatIdxOff = 1 + 1 + 36
	if inter[seatIdxOff] != 0xC8 {
		t.Fatalf("seat_idx 200 not a raw 0xC8 byte at offset %d: inter=%x", seatIdxOff, inter)
	}
	// EncodeServer accepts it (valid §4A frame) and the frame round-trips stably.
	frame, err := EncodeServer(msg)
	if err != nil {
		t.Fatalf("EncodeServer: %v", err)
	}
	back, err := unmarshalServerIntermediate(inter)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Matched.SeatIdx != 200 || back.Matched.StartState.Turn != 200 {
		t.Fatalf("high u8 did not round-trip: seat=%d turn=%d", back.Matched.SeatIdx, back.Matched.StartState.Turn)
	}
	reframed, err := EncodeServer(back)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(frame, reframed) {
		t.Fatalf("high-u8 frame not stable across round-trip")
	}
}

// TestClientSeamHighU8RoundTrip: the client path has no u8 payload field today
// (all client payloads are str/varint/bool), so there is nothing to assert here
// beyond the corpus coverage; documented so the gap is a deliberate note, not an
// oversight. A future client u8 field must add its own high-value case.

// TestEncodeServerRejectsOutOfDomainFromGo exercises the GS-6a write/read
// symmetry fix from the GO side (the live-wire path the slice exists to protect):
// an out-of-domain discriminant in a Go ServerMsg must be REFUSED by EncodeServer,
// not turned into an undecodable §4A frame sent to a real client. The guard lives
// in Rust encode_server; this proves the Go EncodeServer leg actually triggers it.
// The error is a typed codec error (PGC_ERR_MALFORMED -> the generic Err), NOT
// ErrBadIntermediate: the intermediate deserialized fine, the §4A build refused.
func TestEncodeServerRejectsOutOfDomainFromGo(t *testing.T) {
	gid := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	cases := []struct {
		name string
		msg  ServerMsg
	}{
		{"unknown_reason", ServerMsg{Type: SMMoveRejected, MoveRejected: &MoveRejectedPayload{
			GameID: gid, PlyExpected: 1, Reason: 0x99}}},
		{"unknown_termination", ServerMsg{Type: SMGameEnd, GameEnd: &GameEndPayload{
			GameID: gid, ResultVector: []uint8{2, 0}, Termination: 0x99, RatingDelta: []int16{1, -1}}}},
		{"out_of_domain_result", ServerMsg{Type: SMGameEnd, GameEnd: &GameEndPayload{
			GameID: gid, ResultVector: []uint8{7, 0}, Termination: 0x01, RatingDelta: []int16{1, -1}}}},
		{"unknown_error_code", ServerMsg{Type: SMError, Error: &ErrorPayload{Code: 0x99, Msg: "x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := EncodeServer(c.msg)
			if err == nil {
				t.Fatalf("%s: EncodeServer accepted an out-of-domain message — a frame no client could decode", c.name)
			}
			// It must be the typed codec error, NOT ErrBadIntermediate (which is
			// reserved for a corrupt caller-built intermediate, a different fault).
			if err == ErrBadIntermediate {
				t.Fatalf("%s: expected a MalformedPayload-class codec error, got ErrBadIntermediate", c.name)
			}
		})
	}
}

// TestNoClientVariantCarriesU8 pins the "client path has no u8 field" claim so it
// cannot silently rot: if a future ClientMsg variant adds a u8 payload field, its
// Go mirror field will not be exercised by the high-u8 edge test, so this guard
// documents the invariant that lets TestClientSeamHighU8RoundTrip be empty.
// (A compile-time check is not expressible over a tagged-union struct; this is the
// cheapest explicit tripwire — update it, and add a high-u8 client case, together.)
func TestNoClientVariantCarriesU8(t *testing.T) {
	// The 9 §4A.2 client variants and their payload field types:
	//   Authenticate{token:str}  Seek{str,str,bool}  Move{str,u32,str}
	//   OfferDraw/Resign/Abort/Takeback/Rematch{str}  Ping{u64}
	// None carries a u8. If this list changes, add the matching high-u8 round-trip.
	const clientVariantCount = 9
	if int(CMPing)+1 != clientVariantCount {
		t.Fatalf("ClientMsg variant count changed (CMPing=%d) — re-audit for a new u8 field and add a high-u8 case", CMPing)
	}
}
