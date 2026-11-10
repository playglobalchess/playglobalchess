package transport

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/codec"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/session"
)

type captureConn struct{ writes int }

func (c *captureConn) Read(context.Context) ([]byte, error)    { return nil, context.Canceled }
func (c *captureConn) Write(_ context.Context, _ []byte) error { c.writes++; return nil }
func (c *captureConn) Close() error                            { return nil }
func (c *captureConn) RemoteAddr() string                      { return "" }

// TestSendLogsADroppedUnencodableMessage: a message the codec refuses (here a
// STATE that is ENDED with no result vector) is not written, and the drop is
// logged so on-call can find why a client never got its frame.
func TestSendLogsADroppedUnencodableMessage(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	bad := codec.StateBody{
		FEN: "8/8/8/8/8/8/8/8 w - - 0 1", Clocks: []uint32{1, 1}, YouSeat: 0, Ply: 2,
		Status: codec.StatusEnded, ResultVector: []uint8{}, Termination: 2, LastMoveUCI: "e2e4",
		PendingDrawBy: codec.NoSeat, TakebackBy: codec.NoSeat, RunningSeat: codec.NoSeat,
	}
	c := &captureConn{}
	s := &Server{}
	s.send(context.Background(), c, codec.ServerMsg{Type: codec.SMState, State: &codec.StatePayload{GameID: "g", State: bad}})

	if c.writes != 0 {
		t.Fatalf("an unencodable message must not be written, got %d writes", c.writes)
	}
	if !strings.Contains(buf.String(), "dropped unencodable server message") {
		t.Fatalf("the drop must be logged, log was %q", buf.String())
	}
}

// TestTakebackNotAllowedIsProtocolViolation: a TAKEBACK in a rated (or
// non-2-seat) game is a PROTOCOL_VIOLATION, not INTERNAL (GS-7 §5.2.1).
func TestTakebackNotAllowedIsProtocolViolation(t *testing.T) {
	got := mapSessionErr("g", session.ErrTakebackNotAllowed)
	if got.Error == nil || got.Error.Code != codec.ECProtocolViolation {
		t.Fatalf("mapSessionErr(ErrTakebackNotAllowed) = %+v, want PROTOCOL_VIOLATION", got.Error)
	}
}
