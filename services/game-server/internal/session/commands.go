package session

import (
	"context"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/clock"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Snapshot is the projection a client needs to resume a game (§3.5, STATE
// v1.3 §7.4), read from the session's single game.State at one instant.
// RemainingMs is charged up to ServerT for the running seat (#123).
type Snapshot struct {
	FEN         string
	Ply         uint32
	Turn        uint8
	Phase       game.Phase
	RemainingMs []int64
	Ended       bool
	Terminal    game.Termination // meaningful only when Ended
	// v1.3 STATE fields.
	LastMoveUCI     string        // empty iff Ply == 0
	ResultVector    []game.Result // set iff Ended
	DrawOfferBy     int           // game.NoSeat if none
	TakebackBy      int           // game.NoSeat if none
	TakebackAtPly   uint32        // 0 iff TakebackBy is NoSeat
	RunningSeat     int           // game.NoSeat if no clock runs
	ServerT         time.Time     // the instant RemainingMs is valid at
	SeatCount       int
	NextSeq         uint32
	LastChangeTS    uint64
	ClockStartedSet bool
}

// snapshot reads s.st at the session clock's now. Actor goroutine only.
func (s *Session) snapshot() Snapshot {
	return SnapshotOf(s.st, s.now(), s.opts.TimeControl)
}

// SnapshotOf projects a folded state at `now`. It is the one projection the
// live session and the transport's ended-game view (#102, which folds the
// durable stream without standing up a session) both use, so a STATE for an
// ended game is built exactly like a live one.
func SnapshotOf(st game.State, now time.Time, tc clock.TimeControl) Snapshot {
	snap := Snapshot{
		FEN:             st.CurFEN,
		Ply:             st.Ply,
		Turn:            st.TurnIdx,
		Phase:           st.Phase,
		RemainingMs:     st.ClocksAt(uint64(now.UnixMilli()), tc),
		Ended:           st.Phase == game.PhaseEnded,
		Terminal:        st.Termination,
		LastMoveUCI:     st.LastMoveUCI(),
		ResultVector:    append([]game.Result(nil), st.ResultVector...),
		DrawOfferBy:     st.DrawOfferBy,
		TakebackBy:      game.NoSeat,
		RunningSeat:     st.RunningSeat(),
		ServerT:         now,
		SeatCount:       st.SeatCount(),
		NextSeq:         st.NextSeq,
		LastChangeTS:    st.LastChangeTS,
		ClockStartedSet: st.ClockStartedSet,
	}
	if p := st.PendingTakeback; p != nil {
		snap.TakebackBy, snap.TakebackAtPly = int(p.By), p.AtPly
	}
	return snap
}

// StateSnapshot returns the live projection for a reconnect resync.
func (s *Session) StateSnapshot(ctx context.Context) (Snapshot, error) {
	c := &snapCmd{done: make(chan struct{})}
	if err := s.dispatch(ctx, c, c.done); err != nil {
		return Snapshot{}, err
	}
	return c.snap, nil
}

type snapCmd struct {
	snap Snapshot
	done chan struct{}
}

func (c *snapCmd) apply(_ context.Context, s *Session) {
	defer close(c.done)
	c.snap = s.snapshot()
}

// SubmitMove decides one MOVE for the authenticated seat (§4). The serverTS
// argument is ignored: the session's own clock times every move (never trust
// a client clock, T-1/T-2); it is kept for the transport's call shape.
func (s *Session) SubmitMove(ctx context.Context, seat uint8, plyExpected uint32, uci string, _ uint64) (game.MoveOutcome, error) {
	d, _, err := s.run(ctx, game.CmdMove{Seat: seat, PlyExpected: plyExpected, UCI: uci})
	return d.Move, err
}

// TickFlagCheck is the idle flag check the transport drives on a timer (§3.5).
// Returns whether the game ended on this tick.
func (s *Session) TickFlagCheck(ctx context.Context) (bool, error) {
	d, _, err := s.run(ctx, game.CmdTickFlag{})
	return err == nil && d.Effect == game.EffectEnded, err
}

// TickGameOver runs the same flag check and reports whether the game is OVER,
// whether this tick ended it or some other path already had (#88): the ticker
// uses it to repair an ending whose GAME_END broadcast did not go out, instead
// of ticking an ended session forever.
func (s *Session) TickGameOver(ctx context.Context) (bool, error) {
	d, snap, err := s.run(ctx, game.CmdTickFlag{})
	return err == nil && (d.Effect == game.EffectEnded || snap.Ended), err
}
