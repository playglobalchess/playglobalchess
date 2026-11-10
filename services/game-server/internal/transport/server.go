package transport

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// server.go holds the ONLY code that touches the concrete WebSocket library
// (github.com/coder/websocket, Q-GS6a). Everything else in the package depends on the
// Conn interface, so the library choice is isolated here and the whole dispatch
// flow is tested without a socket (dispatch_test.go / flow_test.go / e2e_test.go).

// wsConn adapts a coder/websocket to the Conn interface. Binary frames only (the
// §4A wire is binary). Handle wraps it in a pumpedConn (pump.go), so its Write is
// only ever called by that connection's single writer goroutine, under a
// deadline (#91).
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

// Write sends one §4A frame as a binary message, honouring ctx (the pump's write
// deadline): coder/websocket aborts a write whose ctx expires.
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
	return s.HTTPHandlerWithOrigins(nil)
}

// HTTPHandlerWithOrigins is HTTPHandler with explicit allowed Origin patterns
// (coder/websocket AcceptOptions.OriginPatterns, e.g. "127.0.0.1:*",
// "localhost:*"). Empty/nil = strict same-origin (identical to HTTPHandler).
// Needed when the browser's Origin differs from the WS host — notably a local
// dev run behind the Vite dev-server proxy, where the browser Origin is the
// Vite host (127.0.0.1:5173) but the proxied upstream Host is the server
// (127.0.0.1:7733); without allowing it, coder/websocket rejects with 403 and
// the proxy's upstream write dies with EPIPE. NOT a wide-open "*" in prod.
func (s *Server) HTTPHandlerWithOrigins(originPatterns []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Empty OriginPatterns = strict same-origin (the default). A dev/proxy
			// deploy passes loopback patterns here via HTTPHandlerWithOrigins.
			OriginPatterns: originPatterns,
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
