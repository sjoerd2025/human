package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/mockups"
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

// maxHTTPMethodLen is the longest prefix in httpMethods ("DELETE " and
// "OPTIONS "). server.go peeks this many bytes: the peek must be able to
// contain a WHOLE prefix, or requests under the longest methods silently
// fall into the line-protocol handler.
var maxHTTPMethodLen = func() int {
	max := 0
	for _, m := range httpMethods {
		if len(m) > max {
			max = len(m)
		}
	}
	return max
}()

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

// IsHTTPRequestLine and MaxHTTPMethodLen expose the sniff to server.go,
// which owns the peek that feeds it.
func IsHTTPRequestLine(line []byte) bool { return isHTTPRequestLine(line) }

// MaxHTTPMethodLen is the peek width server.go needs: the longest method
// prefix in httpMethods ("DELETE ", "OPTIONS ").
var MaxHTTPMethodLen = maxHTTPMethodLen

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

		// Consume any request body so the next ReadRequest on a keep-alive
		// connection starts at a real request line. None of this surface's
		// handlers read bodies, so an undrained body would otherwise be
		// parsed as the next request and kill the connection.
		if req.Body != nil {
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
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
	case req.URL.Path == "/api/mockup-sets" && req.Method == http.MethodGet:
		return s.webAPIScanSets(conn, req)
	case strings.HasPrefix(req.URL.Path, "/api/mockup-sets/") && req.Method == http.MethodGet:
		rest := strings.TrimPrefix(req.URL.Path, "/api/mockup-sets/")
		if slug, file, ok := strings.Cut(rest, "/source/"); ok && slug != "" && file != "" {
			return s.webAPISetSource(conn, req, slug, file)
		}
		return s.webAPIScanSet(conn, req, rest)
	case req.URL.Path == "/api/mockup-sets" || strings.HasPrefix(req.URL.Path, "/api/mockup-sets/"):
		return s.writeWebAPI(conn, req, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	default:
		// Unknown path — including anything not under /api. The web API
		// only ever serves the /api subtree the spec declares.
		return s.writeWebAPI(conn, req, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// webAPIProjects lists the projects to scan for mockup sets: the ones this
// daemon has registered (its info file is the same source the board's
// mockupRoots reads), falling back to the daemon's own working directory so a
// project-scoped daemon still answers about its own project.
func (s *Server) webAPIProjects() []mockups.Project {
	if s.WebAPIProjects != nil {
		return s.WebAPIProjects()
	}
	if info, err := ReadInfo(); err == nil && len(info.Projects) > 0 {
		out := make([]mockups.Project, len(info.Projects))
		for i, p := range info.Projects {
			out[i] = mockups.Project{Name: p.Name, Dir: p.Dir}
		}
		return out
	}
	if wd, err := os.Getwd(); err == nil {
		return []mockups.Project{{Name: filepath.Base(wd), Dir: wd}}
	}
	return nil
}

// webAPIScanSets serves GET /api/mockup-sets: every set the daemon's projects
// hold, newest first. Read-only and unauthenticated by the same reasoning as
// /api/healthz: the listener is loopback-only, and the manifests list what a
// `ls mockups/` on the same machine already shows.
func (s *Server) webAPIScanSets(conn net.Conn, req *http.Request) bool {
	sets, _, err := mockups.ScanSets(s.webAPIProjects())
	if err != nil {
		return s.writeWebAPI(conn, req, http.StatusInternalServerError, map[string]string{"error": "scan failed"})
	}
	return s.writeWebAPI(conn, req, http.StatusOK, sets)
}

// webAPIScanSet serves GET /api/mockup-sets/{slug}: one set's manifest and
// the directory it lives in (the desktop's file middleware serves the option
// HTML from disk; the API consumer needs the mapping to build those URLs).
func (s *Server) webAPIScanSet(conn net.Conn, req *http.Request, slug string) bool {
	set, dir, err := mockups.ScanSet(s.webAPIProjects(), slug)
	if errors.Is(err, mockups.ErrSetNotFound) {
		return s.writeWebAPI(conn, req, http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return s.writeWebAPI(conn, req, http.StatusInternalServerError, map[string]string{"error": "scan failed"})
	}
	return s.writeWebAPI(conn, req, http.StatusOK, map[string]any{"set": set, "dir": dir})
}

// webAPISetSource serves GET /api/mockup-sets/{slug}/source/{file}: the raw
// contents of a file the set's manifest lists (in practice a component twin's
// tsx, which the sandbox renders live). Manifest-validated via
// mockups.ReadSetFile — an unlisted path is 404, not a directory read.
func (s *Server) webAPISetSource(conn net.Conn, req *http.Request, slug, name string) bool {
	if req.URL.RawQuery != "" {
		return s.writeWebAPI(conn, req, http.StatusBadRequest, map[string]string{"error": "no query parameters"})
	}
	data, err := mockups.ReadSetFile(s.webAPIProjects(), slug, name)
	if errors.Is(err, mockups.ErrSetNotFound) || errors.Is(err, mockups.ErrFileNotFound) {
		return s.writeWebAPI(conn, req, http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if err != nil {
		return s.writeWebAPI(conn, req, http.StatusInternalServerError, map[string]string{"error": "read failed"})
	}
	return s.writeWebAPIText(conn, req, http.StatusOK, "text/plain; charset=utf-8", data)
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
	return s.writeWebAPIText(conn, req, status, "application/json", b)
}

// writeWebAPIText frames one response with the given content type. It reports
// whether the connection stays open (HTTP/1.1 keep-alive), per req.Close as
// resolved by http.ReadRequest from the Connection header.
func (s *Server) writeWebAPIText(conn net.Conn, req *http.Request, status int, contentType string, b []byte) bool {
	connection := "keep-alive"
	if req.Close {
		connection = "close"
	}
	// HEAD answers with the headers a GET would get (Content-Length
	// included) but never body bytes — writing the body anyway leaves it
	// sitting in the stream, where a keep-alive client mis-frames its next
	// response (or a strict client refuses the response outright).
	payload := b
	if req.Method == http.MethodHead {
		payload = nil
	}
	// #nosec G705 — the body is a manifest-listed project file read back to
	// the same loopback requester that named it (self-echo, no second-party
	// taint); every framing value is a server-side constant.
	_, err := fmt.Fprintf(conn,
		"HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: %s\r\n\r\n%s",
		status, http.StatusText(status), contentType, len(b), connection, payload)
	if err != nil {
		s.Logger.Warn().Err(err).Str("path", req.URL.Path).Msg("web api response write failed")
		return false
	}
	return !req.Close
}
