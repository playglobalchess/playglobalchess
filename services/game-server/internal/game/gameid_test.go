package game

import (
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewGameIDIsV7AndCarriesCreationTime(t *testing.T) {
	before := uint64(time.Now().UnixMilli())
	id, err := NewGameID()
	after := uint64(time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	u, err := uuid.Parse(id)
	if err != nil || u.Version() != 7 {
		t.Fatalf("id %q is not a UUIDv7 (err %v)", id, err)
	}
	if got := GameCreatedMs(id); got < before || got > after {
		t.Fatalf("GameCreatedMs = %d, want within [%d, %d]", got, before, after)
	}
}

func TestGameIDsSortByCreationTime(t *testing.T) {
	var ids []string
	for i := 0; i < 20; i++ {
		id, err := NewGameID()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond) // a distinct millisecond each
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("ids do not sort oldest first: %v", ids)
	}
}

func TestGameCreatedMsIsZeroForOldAndMalformedIDs(t *testing.T) {
	// A UUIDv4 (a game created before GS-8 step 2) and malformed ids give 0
	// (GS-9 section 5: such a game is treated as having lived through every gap).
	for _, id := range []string{uuid.NewString(), "", "not-a-uuid", "game/123"} {
		if got := GameCreatedMs(id); got != 0 {
			t.Errorf("GameCreatedMs(%q) = %d, want 0", id, got)
		}
	}
}

func TestGameCreatedMsKnownVector(t *testing.T) {
	// RFC 9562 Appendix A.6 example: unix_ts_ms 0x017F22E279B0.
	const id = "017f22e2-79b0-7cc3-98c4-dc0c0c07398f"
	if got, want := GameCreatedMs(id), uint64(0x017F22E279B0); got != want {
		t.Fatalf("GameCreatedMs = %#x, want %#x", got, want)
	}
}
