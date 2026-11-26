package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Test support for the persistence invariant (#99).
//
// Every session test that writes events MUST end with assertRebuildMatches:
// the stream the session wrote must fold, and a fresh NewSession over it must
// equal the live projection. seedLive's returned cancel func already does this,
// so a test that `defer cancel()`s gets it for free; a new session command's
// test must not bypass it (use seedLiveRaw only where the cancel func runs on
// the actor itself). FaultStore covers the failures a happy-path store hides.

// rebuildMismatch returns "" when the stream folds (pure and with the engine)
// and a fresh session over it equals live, else what differs. It reports
// errSessionClosed as (_, false): a closed session has nothing to compare.
func rebuildMismatch(s *Session, store game.EventStore) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := store.Load(ctx, game.StreamIDFor(s.gameID))
	if err != nil {
		return fmt.Sprintf("store load: %v", err), true
	}
	pure, err := game.Fold(events, s.opts)
	if err != nil {
		return fmt.Sprintf("stream does not fold: %v", err), true
	}
	if _, err := game.FoldWithEngine(ctx, events, s.opts, s.oracle); err != nil {
		return fmt.Sprintf("stream does not fold with the engine: %v", err), true
	}
	liveSt, err := readState(ctx, s)
	if errors.Is(err, errSessionClosed) {
		return "", false
	}
	if err != nil {
		return fmt.Sprintf("live state: %v", err), true
	}
	liveSnap, err := s.StateSnapshot(ctx)
	if err != nil {
		return fmt.Sprintf("live snapshot: %v", err), true
	}
	rb, err := NewSession(ctx, Config{GameID: s.gameID, Store: store, Oracle: s.oracle, Opts: s.opts, Now: s.now, AbortBeforePly: s.abortBeforePly})
	if err != nil {
		return fmt.Sprintf("a fresh session cannot be built from the stream: %v", err), true
	}
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	go rb.Run(rctx)
	rbSt, err := readState(ctx, rb)
	if err != nil {
		return fmt.Sprintf("rebuilt state: %v", err), true
	}
	rbSnap, err := rb.StateSnapshot(ctx)
	if err != nil {
		return fmt.Sprintf("rebuilt snapshot: %v", err), true
	}
	var bad []string
	if d := fieldDiff(liveSt, rbSt); d != "" {
		bad = append(bad, "live != rebuilt State: "+d)
	}
	// The clock-dependent snapshot fields are covered by the State comparison
	// (the clock anchors live in State); under a real clock two reads differ.
	for _, sn := range []*Snapshot{&liveSnap, &rbSnap} {
		sn.ServerT, sn.RemainingMs = time.Time{}, nil
	}
	if fmt.Sprintf("%+v", liveSnap) != fmt.Sprintf("%+v", rbSnap) {
		bad = append(bad, fmt.Sprintf("live != rebuilt Snapshot:\n live %+v\n rb   %+v", liveSnap, rbSnap))
	}
	if d := pureDiff(liveSt, pure); d != "" {
		bad = append(bad, "live != pure Fold: "+d)
	}
	return strings.Join(bad, "; "), true
}

// assertRebuildMatches fails t when rebuildMismatch finds a difference.
func assertRebuildMatches(t *testing.T, s *Session, store game.EventStore) {
	t.Helper()
	if msg, checked := rebuildMismatch(s, store); checked && msg != "" {
		t.Errorf("persistence invariant (#99): %s", msg)
	}
}

// FaultStore wraps an EventStore with one-shot faults on the write and read
// paths. Each arm applies to the next matching call (Append and AppendBatch
// both), then clears.
type FaultStore struct {
	game.EventStore

	mu          sync.Mutex
	commitThen  error // commit, then return this error
	errBefore   error // return this error, commit nothing
	staleReads  int   // next Load drops this many trailing events
	faultsFired int
}

func NewFaultStore(inner game.EventStore) *FaultStore { return &FaultStore{EventStore: inner} }

// CommitThenError makes the next write commit and then report err (an
// ambiguous commit: a timeout after the store accepted the write).
func (f *FaultStore) CommitThenError(err error) { f.mu.Lock(); f.commitThen = err; f.mu.Unlock() }

// ErrorBeforeCommit makes the next write fail without committing.
func (f *FaultStore) ErrorBeforeCommit(err error) { f.mu.Lock(); f.errBefore = err; f.mu.Unlock() }

// StaleReads makes the next Load return the stream minus its last n events.
func (f *FaultStore) StaleReads(n int) { f.mu.Lock(); f.staleReads = n; f.mu.Unlock() }

// Fired reports how many armed faults have triggered.
func (f *FaultStore) Fired() int { f.mu.Lock(); defer f.mu.Unlock(); return f.faultsFired }

// take returns the fault armed for a write, clearing it.
func (f *FaultStore) take() (commitThen, errBefore error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	commitThen, errBefore = f.commitThen, f.errBefore
	if commitThen != nil || errBefore != nil {
		f.faultsFired++
	}
	f.commitThen, f.errBefore = nil, nil
	return commitThen, errBefore
}

func (f *FaultStore) Append(ctx context.Context, id string, seq uint32, ev game.Event) error {
	then, before := f.take()
	if before != nil {
		return before
	}
	err := f.EventStore.Append(ctx, id, seq, ev)
	if err == nil && then != nil {
		return then
	}
	return err
}

func (f *FaultStore) AppendBatch(ctx context.Context, id string, seq uint32, evs []game.Event) error {
	then, before := f.take()
	if before != nil {
		return before
	}
	err := f.EventStore.AppendBatch(ctx, id, seq, evs)
	if err == nil && then != nil {
		return then
	}
	return err
}

func (f *FaultStore) Load(ctx context.Context, id string) ([]game.Event, error) {
	events, err := f.EventStore.Load(ctx, id)
	f.mu.Lock()
	n := f.staleReads
	if n > 0 {
		f.staleReads = 0
		f.faultsFired++
	}
	f.mu.Unlock()
	if err == nil && n > 0 {
		if n > len(events) {
			n = len(events)
		}
		events = events[:len(events)-n]
	}
	return events, err
}
