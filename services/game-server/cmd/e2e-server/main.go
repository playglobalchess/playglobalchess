// Command e2e-server boots the real GS-6 game-server stack with an in-memory
// matchmaking pool (no Redis) and serves the §4A WebSocket edge on a loopback
// address. It exists so an end-to-end harness — notably the web client's e2e —
// can drive the FULL real server over a real socket without standing up Redis
// or DynamoDB. It is NOT a production entrypoint: the in-memory pool and
// MemStore are single-node and non-durable, and DevAuthenticator treats the
// token verbatim as the user id. A production node wires RedisPool + the
// durable event store + a real authenticator.
//
// Usage:
//
//	e2e-server -addr 127.0.0.1:7733 -path /ws
//
// Prints "listening <addr><path>" once ready (the harness waits for this line),
// then blocks until killed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/playglobalchess/playglobalchess/services/game-server/internal/engine"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/game"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/matchmaking"
	"github.com/playglobalchess/playglobalchess/services/game-server/internal/transport"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7733", "loopback host:port to listen on")
	path := flag.String("path", "/ws", "WebSocket path")
	flag.Parse()

	oracle, err := engine.New()
	if err != nil {
		log.Fatalf("e2e-server: engine.New: %v", err)
	}
	store := game.NewMemStore()
	pool := matchmaking.NewMemoryPool()
	mm := matchmaking.New(pool, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := transport.NewRegistry(ctx, store, oracle, transport.ConventionResolver{})
	srv := transport.NewServer(transport.DevAuthenticator{}, mm, reg)

	mux := http.NewServeMux()
	mux.Handle(*path, srv.HTTPHandler())
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Announce readiness on stdout AFTER the listener is bound, so a harness can
	// wait for this exact line rather than racing the socket.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("e2e-server: listen %s: %v", *addr, err)
	}
	fmt.Printf("listening %s%s\n", *addr, *path)
	os.Stdout.Sync()

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("e2e-server: serve: %v", err)
	}
}
