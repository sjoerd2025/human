package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// The web API is the Express-shaped HTTP surface mounted on the daemon's
// existing listener (combine plan Seam 2). The React sandbox under web/
// generates its client from web/lib/api-spec/openapi.yaml, whose servers URL
// is /api — so the sandbox (and, once embedded, the Wails app's /mocks/ page)
// talks HTTP to the same 127.0.0.1:19285 socket the CLI line protocol uses.
// One port, one listener, no second daemon process.
//
// The surface is intentionally token-free: the listener is loopback-only and
// the routes are read-only status endpoints. Anything mutating stays behind
// the line protocol's token check.

// httpMethods are the request-line prefixes that mark a connection as HTTP
// rather than the CLI's JSON line protocol. The line protocol's first byte is
// always '{', so there is no ambiguity.
var httpMethods = [][]byte{
	[]byte(http.MethodGet + " "),
	[]byte(http.MethodPost + " "),
	[]byte(http.MethodPut + " "),
	[]byte(http.MethodPatch + " "),
	[]byte(http.MethodDelete + " "),
	[]byte(http.MethodHead + " "),
	[]byte(http.MethodOptions + " "),
}

// isHTTPRequestLine reports whether the first line read off a connection is
// an HTTP request line ("GET /api/healthz HTTP/1.1") rather than a JSON
// protocol request.
func isHTTPRequestLine(line []byte) bool {
	for _, m := range httpMethods {
		if bytes.HasPrefix(line, m) {
			return true
		}
	}
	return false
}

// serveWebAPIConn answers HTTP requests on a connection detected by
// isHTTPRequestLine, then returns (the caller closes the conn). Requests are
// parsed with http.ReadRequest and answered inline instead of via a per-conn
// http.Server: keep-alive connections would otherwise pin one of the accept
// loop's 64 worker slots indefinitely. Responses are hand-framed with
// Content-Length, which is all this fixed-JSON surface needs.
//
// The reader already wraps a 1 MiB LimitReader (set up for the line
// protocol's request bound), so it caps a keep-alive connection's total HTTP
// traffic too — thousands of health checks; harmless for this surface.
func (s *Server) serveWebAPIConn(conn net.Conn, reader *bufio.Reader) {
	defer func() { _ = conn.Close() }()

	for {
		// Bound an idle keep-alive connection so it releases the worker
		// slot; reset at the top of each request.
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		req, err := http.ReadRequest(reader)
		if err != nil {
			return // EOF between requests, or garbage: drop the conn
		}

		keepOpen := s.routeWebAPI(conn, req)
		if !keepOpen || req.Close {
			return
		}
	}
}

// routeWebAPI dispatches one HTTP request and writes the response. It reports
// whether the connection may be reused for another request.
func (s *Server) routeWebAPI(conn net.Conn, req *http.Request) bool {
	switch {
	case req.URL.Path == "/api/healthz" && req.Method == http.MethodGet:
		return s.writeWebAPI(conn, req, http.StatusOK, map[string]string{"status": "ok"})
	case req.URL.Path == "/api/healthz":
		return s.writeWebAPI(conn, req, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	default:
		// Unknown path — including anything not under /api. The web API
		// only ever serves the /api subtree the spec declares.
		return s.writeWebAPI(conn, req, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// writeWebAPI frames one JSON response. It reports whether the connection
// stays open (HTTP/1.1 keep-alive), per req.Close as resolved by
// http.ReadRequest from the Connection header.
func (s *Server) writeWebAPI(conn net.Conn, req *http.Request, status int, body any) bool {
	b, err := json.Marshal(body)
	if err != nil {
		b = []byte(`{"error":"internal error"}`)
		status = http.StatusInternalServerError
	}
	connection := "keep-alive"
	if req.Close {
		connection = "close"
	}
	_, err = fmt.Fprintf(conn,
		"HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: %s\r\n\r\n%s",
		status, http.StatusText(status), len(b), connection, b)
	if err != nil {
		s.Logger.Warn().Err(err).Str("path", req.URL.Path).Msg("web api response write failed")
		return false
	}
	return !req.Close
}
