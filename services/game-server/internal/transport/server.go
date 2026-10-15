package transport

import (
	"context"
	"net/http"
	"time"

	"nhooyr.io/websocket"
)

// server.go holds the ONLY code that touches the concrete WebSocket library
// (nhooyr.io/websocket, Q-GS6a). Everything else in the package depends on the
// Conn interface, so the library choice is isolated here and the whole dispatch
// flow is tested without a socket (dispatch_test.go / flow_test.go / e2e_test.go).

// wsConn adapts a nhooyr WebSocket to the Conn interface. Writes are serialized
// by nhooyr internally per message; we additionally never issue concurrent
// Writes from the same goroutine. Binary frames only (the §4A wire is binary).
type wsConn struct {
	ws   *websocket.Conn
	addr string
}

// Read blocks for the next binary message (one §4A frame per message).
func (c *wsConn) Read(ctx context.Context) ([]byte, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		// The §4A wire is binary; a text frame is a protocol violation. Return the
		// bytes anyway so the codec rejects it uniformly (decode will fail → the
		// dispatch loop replies PROTOCOL_VIOLATION).
		return data, nil
	}
	return data, nil
}

// Write sends one §4A frame as a binary message. nhooyr serializes writes, so
// this is safe for the concurrent caller (the MATCHED fan-out to the other seat).
func (c *wsConn) Write(ctx context.Context, frame []byte) error {
	return c.ws.Write(ctx, websocket.MessageBinary, frame)
}

// Close tears the socket down (idempotent-ish; a double close is harmless).
func (c *wsConn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "bye")
}

func (c *wsConn) RemoteAddr() string { return c.addr }

// HTTPHandler returns an http.Handler that upgrades each request to a WebSocket
// and runs the per-connection dispatch loop (Server.Handle). Mount it at the WS
// path (e.g. "/ws") behind TLS/origin checks (config/infra, §7). One goroutine
// per connection (ADR-12) — net/http already gives us that per request.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Subprotocol/compression left default; origin checks are config/infra
			// (the server binds per ADR-12; a production deploy sets OriginPatterns
			// or terminates at a trusted proxy).
		})
		if err != nil {
			return // Accept already wrote the HTTP error.
		}
		// A generous read limit for a resync STATE with a long FEN; the move hot
		// path is tiny. The §4A 4 KB cap is enforced by the codec on decode.
		ws.SetReadLimit(16 * 1024)
		conn := &wsConn{ws: ws, addr: r.RemoteAddr}
		// The request context is cancelled when the client disconnects; bound the
		// connection's lifetime to it.
		ctx := r.Context()
		s.Handle(ctx, conn)
	})
}

// ListenAndServe runs an HTTP server mounting the WS handler at path on addr.
// Convenience for a standalone node; a larger deploy mounts HTTPHandler on its
// own mux. Blocks until the server stops. Always bind to an explicit addr.
func (s *Server) ListenAndServe(addr, path string) error {
	mux := http.NewServeMux()
	mux.Handle(path, s.HTTPHandler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}
