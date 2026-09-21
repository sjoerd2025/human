package daemon

import (
	"bufio"
	"context"
	"encoding/json"
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
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
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
