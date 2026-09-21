package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startWebAPIServer spins a daemon on an ephemeral port with the same shape
// the route tests use, for HTTP-surface tests.
func startWebAPIServer(t *testing.T) (addr string, token string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      "tok",
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr = ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			t.Cleanup(cancel)
			return addr, srv.Token
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The probe expiring means the daemon never came up (or took the port
	// from the closed probe listener and lost it): fail loudly instead of
	// letting every dial in the test mis-report as a server defect.
	cancel()
	t.Fatalf("daemon did not accept connections on %s within 2s", addr)
	return addr, srv.Token
}

func TestIsHTTPRequestLine(t *testing.T) {
	assert.True(t, isHTTPRequestLine([]byte("GET /api/healthz HTTP/1.1\r\n")))
	assert.True(t, isHTTPRequestLine([]byte("POST /api/x HTTP/1.1\r\n")))
	assert.True(t, isHTTPRequestLine([]byte("HEAD / HTTP/1.1")))
	assert.False(t, isHTTPRequestLine([]byte(`{"token":"tok"`)))
	assert.False(t, isHTTPRequestLine([]byte("XGET / HTTP/1.1")))
}

// TestWebAPIHealthzOverListener proves the sniff branch serves HTTP on the
// same port the line protocol owns.
func TestWebAPIHealthzOverListener(t *testing.T) {
	addr, _ := startWebAPIServer(t)

	res, err := http.Get("http://" + addr + "/api/healthz")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))

	var body struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	assert.Equal(t, "ok", body.Status)
}

// TestWebAPIKeepAlive proves two requests ride one connection (the inline
// response framing must resolve keep-alive correctly).
func TestWebAPIKeepAlive(t *testing.T) {
	addr, _ := startWebAPIServer(t)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		_, werr := conn.Write([]byte("GET /api/healthz HTTP/1.1\r\nHost: x\r\n\r\n"))
		require.NoError(t, werr)
		res, rerr := http.ReadResponse(reader, nil)
		require.NoError(t, rerr)
		b, rerr := io.ReadAll(res.Body)
		require.NoError(t, rerr)
		assert.Equal(t, http.StatusOK, res.StatusCode)
		assert.Contains(t, string(b), `"status":"ok"`)
	}
}

// TestWebAPIMethodPrefixes proves the sniff catches every advertised method:
// the peek must hold a whole method prefix, or the longest ones (PATCH,
// DELETE, OPTIONS) would silently fall into the line-protocol handler.
func TestWebAPIMethodPrefixes(t *testing.T) {
	addr, _ := startWebAPIServer(t)
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions,
	} {
		req, err := http.NewRequest(method, "http://"+addr+"/api/healthz", nil)
		require.NoError(t, err)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err, method)
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		// Any well-framed HTTP answer (200/404/405) proves the sniff worked;
		// a fall-through would leave the client with a transport error.
		assert.Contains(t, []int{http.StatusOK, http.StatusNotFound, http.StatusMethodNotAllowed}, res.StatusCode, method)
	}
}

// TestWebAPIKeepAliveBodyDrained proves a request carrying a body does not
// poison the next request on the same connection.
func TestWebAPIKeepAliveBodyDrained(t *testing.T) {
	addr, _ := startWebAPIServer(t)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	_, werr := fmt.Fprintf(conn, "POST /api/healthz HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")
	require.NoError(t, werr)
	res, rerr := http.ReadResponse(reader, nil)
	require.NoError(t, rerr)
	_, _ = io.Copy(io.Discard, res.Body)
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)

	_, werr = conn.Write([]byte("GET /api/healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	require.NoError(t, werr)
	res2, rerr := http.ReadResponse(reader, nil)
	require.NoError(t, rerr)
	b, _ := io.ReadAll(res2.Body)
	assert.Equal(t, http.StatusOK, res2.StatusCode)
	assert.Contains(t, string(b), `"status":"ok"`)
}

// TestWebAPIHeadHasNoBody proves a HEAD response is header-only, so the
// next keep-alive response starts where the client expects it.
func TestWebAPIHeadHasNoBody(t *testing.T) {
	addr, _ := startWebAPIServer(t)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	_, werr := conn.Write([]byte("HEAD /api/healthz HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, werr)
	// ReadResponse needs the request so it knows a HEAD response carries no
	// body despite Content-Length.
	headReq, herr := http.NewRequest(http.MethodHead, "http://"+addr+"/api/healthz", nil)
	require.NoError(t, herr)
	res, rerr := http.ReadResponse(reader, headReq)
	require.NoError(t, rerr)
	_, _ = io.Copy(io.Discard, res.Body)
	// /api/healthz is GET-only by route table, so HEAD answers 405 — the
	// point here is that the response is well-framed and bodyless, leaving
	// the keep-alive connection usable for the next request.
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)
	assert.Equal(t, "application/json", res.Header.Get("Content-Type"))

	_, werr = conn.Write([]byte("GET /api/healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	require.NoError(t, werr)
	res2, rerr := http.ReadResponse(reader, nil)
	require.NoError(t, rerr)
	_, _ = io.Copy(io.Discard, res2.Body)
	assert.Equal(t, http.StatusOK, res2.StatusCode)
}

// TestWebAPIRejections covers the non-happy paths.
func TestWebAPIRejections(t *testing.T) {
	addr, _ := startWebAPIServer(t)

	res, err := http.Post("http://"+addr+"/api/healthz", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)

	res2, err := http.Get("http://" + addr + "/api/nope")
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, res2.Body)
	_ = res2.Body.Close()
	assert.Equal(t, http.StatusNotFound, res2.StatusCode)
}

// TestLineProtocolUnaffected proves the CLI protocol still works on the same
// port after the sniff — a JSON line gets the normal token'd response.
func TestLineProtocolUnaffected(t *testing.T) {
	addr, token := startWebAPIServer(t)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"status"}})
	// "status" isn't routed here, so it exits non-zero — but crucially it got
	// a line-protocol response, not an HTTP error page.
	assert.NotContains(t, resp.Stderr, "HTTP/1.1")
}
