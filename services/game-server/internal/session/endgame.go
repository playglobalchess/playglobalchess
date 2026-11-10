package session

import (
	"context"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
)

// Resign, abort, draw and takeback commands (GS-3b, amended by GS-7 §5–§6).
// Each is one decide/evolve/append through exec; the rules live in game.Decide.

// Resign ends the game as a loss for seat by. A resend after the end is a
// no-op success.
func (s *Session) Resign(ctx context.Context, by uint8) error {
	_, _, err := s.run(ctx, game.CmdResign{By: by})
	return err
}

// Abort ends an unstarted game (ply < abortBeforePly) with no result; past
// that it returns ErrAbortTooLate.
func (s *Session) Abort(ctx context.Context, by uint8) error {
	_, _, err := s.run(ctx, game.CmdAbort{By: by, AbortBeforePly: s.abortBeforePly})
	return err
}

// DrawResult reports what an OFFER_DRAW or DECLINE_DRAW did.
type DrawResult struct {
	Effect game.Effect // EffectDrawOffered, EffectEnded (agreed), EffectDrawDeclined, EffectIgnored, EffectNone
	Snap   Snapshot    // the state right after the command
}

// OfferDraw is OFFER_DRAW (§6.3): it accepts the other seat's pending offer,
// is a no-op on a repeat, and otherwise offers if the rules allow.
func (s *Session) OfferDraw(ctx context.Context, by uint8) error {
	_, err := s.OfferDrawResult(ctx, by)
	return err
}

// OfferDrawResult is OfferDraw, returning what happened.
func (s *Session) OfferDrawResult(ctx context.Context, by uint8) (DrawResult, error) {
	d, snap, err := s.run(ctx, game.CmdOfferDraw{By: by})
	return DrawResult{Effect: d.Effect, Snap: snap}, err
}

// AcceptDraw accepts the other seat's pending offer, or returns
// ErrNoDrawToAccept.
func (s *Session) AcceptDraw(ctx context.Context, by uint8) error {
	_, _, err := s.run(ctx, game.CmdAcceptDraw{By: by})
	return err
}

// DeclineDraw is DECLINE_DRAW: the responder declines or the offerer withdraws.
func (s *Session) DeclineDraw(ctx context.Context, by uint8) (DrawResult, error) {
	d, snap, err := s.run(ctx, game.CmdDeclineDraw{By: by})
	return DrawResult{Effect: d.Effect, Snap: snap}, err
}

// TakebackResult reports what a takeback command did.
type TakebackResult struct {
	Effect game.Effect // EffectTakebackProposed / Rewound / Declined, EffectEnded (flag), EffectIgnored, EffectNone
	AtPly  uint32      // the request's at_ply, for EffectTakebackProposed
	Snap   Snapshot
}

// ProposeTakeback is TAKEBACK (request only, §5.2 step 1).
func (s *Session) ProposeTakeback(ctx context.Context, by uint8) (TakebackResult, error) {
	d, snap, err := s.run(ctx, game.CmdProposeTakeback{By: by})
	return TakebackResult{Effect: d.Effect, AtPly: d.AtPly, Snap: snap}, err
}

// AcceptTakeback is TAKEBACK_ACCEPT{at_ply} (§5.2 step 2).
func (s *Session) AcceptTakeback(ctx context.Context, by uint8, atPly uint32) (TakebackResult, error) {
	d, snap, err := s.run(ctx, game.CmdAcceptTakeback{By: by, AtPly: atPly})
	return TakebackResult{Effect: d.Effect, Snap: snap}, err
}

// DeclineTakeback is TAKEBACK_DECLINE{at_ply}: decline by the responder or
// cancel by the proposer (§5.2 step 3).
func (s *Session) DeclineTakeback(ctx context.Context, by uint8, atPly uint32) (TakebackResult, error) {
	d, snap, err := s.run(ctx, game.CmdDeclineTakeback{By: by, AtPly: atPly})
	return TakebackResult{Effect: d.Effect, Snap: snap}, err
}
