package game

import (
	"errors"
	"testing"
)

// An event type this build does not know is reported as ErrUnknownEvent, so
// callers can tell a stream a newer build wrote from a corrupt one and never
// delete index rows on it (PR #234 review, S6-a).
func TestEvolveUnknownEventIsErrUnknownEvent(t *testing.T) {
	_, err := Evolve(initialState(), Event{Type: EventType(250)}, nil, FoldOptions{})
	if !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("Evolve of an unknown event type: %v, want ErrUnknownEvent", err)
	}
}
