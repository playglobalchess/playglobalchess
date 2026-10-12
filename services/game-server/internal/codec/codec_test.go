package codec

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Committed golden frames — a subset of protocol/vectors/vectors.json, mirroring
// protocol/codec-ffi/tests/codec_parity_oracle.rs and the mobile/web parity
// tests. Change in lockstep (the parity contract). This test is the Go leg of
// NFR-10: the SAME codec, linked into Go via cgo, reproduces the frozen bytes
// byte-for-byte — completing parity across Rust + iOS + Android + web + Go.
const (
	clientMovePromoHex    = "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871"
	serverGameEndHex      = "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817"
	eventRatingAppliedHex = "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d"
)

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

func TestGoldenVectorParity(t *testing.T) {
	cases := []struct {
		name string
		fn   func([]byte) ([]byte, error)
		hex  string
	}{
		{"client/move_promotion", ReencodeClient, clientMovePromoHex},
		{"server/game_end", ReencodeServer, serverGameEndHex},
		{"event/rating_applied", ReencodeEvent, eventRatingAppliedHex},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := mustHex(t, c.hex)
			out, err := c.fn(in)
			if err != nil {
				t.Fatalf("reencode failed: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Fatalf("decode->re-encode not byte-identical\n in: %x\nout: %x", in, out)
			}
		})
	}
}

func TestMalformedIsErrorNotCrash(t *testing.T) {
	// A decode failure must be a returned error, never a crash/panic across cgo.
	if _, err := ReencodeClient([]byte{}); err == nil {
		t.Error("empty frame: expected error, got nil")
	}
	if _, err := ReencodeServer([]byte{0x01, 0x09}); err == nil {
		t.Error("wrong-direction frame: expected error, got nil")
	}
	if _, err := ReencodeEvent([]byte{0xff}); err == nil {
		t.Error("bad event: expected error, got nil")
	}
}

func TestProtocolVersion(t *testing.T) {
	if v := ProtocolVersion(); v != 1 {
		t.Errorf("protocol version = %d, want 1", v)
	}
}

// TestNoLeakSmoke runs many reencodes to shake out an obvious free imbalance
// (a real leak check uses the Rust-side counting-allocator / asan test, §8;
// this is a cheap Go-side smoke that the happy path frees what it allocates).
func TestNoLeakSmoke(t *testing.T) {
	in := mustHex(t, clientMovePromoHex)
	for i := 0; i < 100000; i++ {
		out, err := ReencodeClient(in)
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("iteration %d: err=%v", i, err)
		}
	}
}
