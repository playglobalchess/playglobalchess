package codec

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Anti-drift guard (addresses the GS-4a mirror-coupling concern). The Go
// postcard subset + the game.Event⇄EventEnvelope mapping mirror Rust's
// #[derive(Serialize)] and the Rust enum's DECLARATION ORDER. Hand-copied hex
// fixtures (event_seam_test.go) would silently go stale if Rust reorders a
// variant / changes a field and the fixtures are not re-captured in the same
// change. This test removes that hazard: it reads the SHARED, Rust-generated
// corpus (protocol/vectors/vectors.json — regenerated only via
// `cargo run --example gen_vectors`, guarded by the Rust golden_freeze test) and
// round-trips every committed §4A EVENT frame through the REAL seam:
//
//	DecodeEvent(frame) -> (hdr, ev) -> EncodeEvent(hdr, ev) == frame
//
// So a Rust change that regenerates vectors.json flows into this test with no
// hand edit; if the Go mirror drifts from the regenerated corpus, the frame will
// not reproduce and this test fails. The two implementations are bound by a
// generated artifact, not by copied bytes.
//
// Path: this test file lives in services/game-server/internal/codec; the shared
// corpus is at <repo>/protocol/vectors/vectors.json.
const sharedVectorsRelPath = "../../../../protocol/vectors/vectors.json"

type sharedVector struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Hex  string `json:"hex"`
}

type sharedVectorsFile struct {
	Vectors []sharedVector `json:"vectors"`
}

func TestEventFramesFromSharedCorpusRoundTrip(t *testing.T) {
	path := filepath.Clean(sharedVectorsRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared corpus %s: %v (regenerate via `cargo run --example gen_vectors`)", path, err)
	}
	var f sharedVectorsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	events := 0
	for _, v := range f.Vectors {
		if v.Kind != "event" {
			continue
		}
		events++
		t.Run(v.Name, func(t *testing.T) {
			frame, err := hex.DecodeString(v.Hex)
			if err != nil {
				t.Fatalf("bad hex in corpus: %v", err)
			}
			hdr, ev, err := DecodeEvent(frame)
			if err != nil {
				t.Fatalf("DecodeEvent(%s): %v — the Go mapping may have drifted from the regenerated Rust corpus", v.Name, err)
			}
			reframed, err := EncodeEvent(hdr, ev)
			if err != nil {
				t.Fatalf("EncodeEvent(%s): %v", v.Name, err)
			}
			if hex.EncodeToString(reframed) != v.Hex {
				t.Fatalf("%s: Go seam did not reproduce the committed §4A frame — Go mirror has drifted from Rust.\n committed=%s\n got      =%s", v.Name, v.Hex, hex.EncodeToString(reframed))
			}
		})
	}

	// If Rust ever stops emitting event vectors, this guard would silently pass
	// on zero cases — assert the corpus actually carried some.
	if events == 0 {
		t.Fatalf("shared corpus %s contains no kind=event vectors — anti-drift guard would be vacuous", path)
	}
	// Sanity on count so a shrink is noticed (12 variants; the corpus carries at
	// least one frame per variant plus edge cases — it had 15 at authoring).
	if events < 12 {
		t.Fatalf("expected >=12 event vectors in the shared corpus, got %d — did gen_vectors drop coverage?", events)
	}
}
