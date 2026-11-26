package transport

import (
	"fmt"
	"testing"
)

func bindMany(s *Server, c Conn, n int) {
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("g%d", i)
		s.bindSeatForUser(c, fmt.Sprintf("a%d", i), gid, 0)
		s.bindSeatForUser(c, fmt.Sprintf("b%d", i), gid, 1)
	}
}

// Disconnect visits only the games the user is seated in, and also clears seats
// bound through bindSeatForUser (MATCHED delivery), which never reach st.seats.
func TestUnbindAllClearsOnlyThisUsersSeats(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	s := h.srv
	mine, other := newFakeConn(), newFakeConn()
	bindMany(s, other, 50)
	s.bindSeatForUser(mine, "me", "mine1", 0)
	s.bindSeatForUser(other, "opp", "mine1", 1)
	s.bindSeatForUser(mine, "me", "mine2", 1)
	s.userMu.Lock()
	s.userConns["me"] = mine
	s.userMu.Unlock()

	s.unbindAll(&connState{userID: "me"}, mine)

	for _, gs := range []gameSeAT{{"mine1", 0}, {"mine2", 1}} {
		if _, ok := s.connFor(gs.gameID, gs.seat); ok {
			t.Fatalf("%v still bound after its connection left", gs)
		}
	}
	if c, ok := s.connFor("mine1", 1); !ok || c != other {
		t.Fatal("the opponent's seat in the same game was cleared")
	}
	for i := 0; i < 50; i++ {
		if _, ok := s.connFor(fmt.Sprintf("g%d", i), 0); !ok {
			t.Fatalf("unrelated game g%d lost its binding", i)
		}
	}
	if _, ok := s.userConn("me"); ok {
		t.Fatal("userConns kept the departed connection")
	}
	s.userMu.Lock()
	_, kept := s.userGames["me"]["mine1"]
	s.userMu.Unlock()
	if !kept {
		t.Fatal("the reconnect index was dropped on disconnect")
	}
}

// A reconnect rebinds the seat; the old connection's late teardown must not
// clear the new binding.
func TestUnbindAllKeepsAReboundSeat(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	s := h.srv
	oldC, newC := newFakeConn(), newFakeConn()
	s.bindSeatForUser(oldC, "me", "g", 0)
	s.bindSeatForUser(newC, "me", "g", 0)
	s.unbindAll(&connState{userID: "me"}, oldC)
	if c, ok := s.connFor("g", 0); !ok || c != newC {
		t.Fatal("the old connection's teardown cleared the rebound seat")
	}
}

// dropGame clears exactly one game's state, found through the game's own entry.
func TestDropGameClearsOnlyThatGame(t *testing.T) {
	h := newHarness(t)
	defer h.cancel()
	s := h.srv
	c := newFakeConn()
	bindMany(s, c, 20)
	s.registerJoin("g3")
	if !s.claimGameEnd("g3") {
		t.Fatal("claim")
	}
	s.dropGame("g3")
	if s.gt.tracked("g3") {
		t.Fatal("game state survived dropGame")
	}
	s.userMu.Lock()
	_, a := s.userGames["a3"]
	_, b := s.userGames["b3"]
	_, other := s.userGames["a4"]["g4"]
	s.userMu.Unlock()
	if a || b || !other {
		t.Fatalf("user index after drop: a3=%v b3=%v a4/g4=%v", a, b, other)
	}
	if !s.claimGameEnd("g3") {
		t.Fatal("claim latch survived dropGame")
	}
	s.releaseGameEnd("g3")
	if s.gt.tracked("g3") {
		t.Fatal("releasing the only claim left an empty entry behind")
	}
}

// Benchmarks for #174: per-move and per-ending lookups must not grow with the
// number of unrelated games and connections on the node.
func benchServer(b *testing.B, n int) (*Server, Conn) {
	h := newHarness(b)
	b.Cleanup(h.cancel)
	c := newFakeConn()
	bindMany(h.srv, c, n)
	h.srv.bindSeatForUser(c, "me", "hot", 0)
	return h.srv, c
}

func BenchmarkMoveLookup(b *testing.B) {
	for _, n := range []int{100, 10_000, 200_000} {
		b.Run(fmt.Sprintf("games=%d", n), func(b *testing.B) {
			s, c := benchServer(b, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := s.seatOf(c, "hot"); !ok {
					b.Fatal("not seated")
				}
				s.connFor("hot", 0)
				s.seatUsers("hot")
			}
		})
	}
}

func BenchmarkEndAndDisconnect(b *testing.B) {
	for _, n := range []int{100, 10_000, 200_000} {
		b.Run(fmt.Sprintf("games=%d", n), func(b *testing.B) {
			s, c := benchServer(b, n)
			st := &connState{userID: "me"}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.bindSeatForUser(c, "me", "hot", 0)
				s.claimGameEnd("hot")
				s.dropGame("hot")
				s.unbindAll(st, c)
			}
		})
	}
}
