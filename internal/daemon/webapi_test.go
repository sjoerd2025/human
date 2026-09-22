package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/mockups"
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

// startMockupAPIServer is startWebAPIServer plus a project override pointing
// the /api mockup surface at a temp fixture layout:
//
//	projA/mockups/alpha/index.json (+2 options)
//	projA/mockups/beta/index.json  (newer than alpha)
//	projB/mockups/alpha/index.json (duplicate slug — first project wins)
func startMockupAPIServer(t *testing.T) (addr string, projA, projB string) {
	t.Helper()
	projA, projB = t.TempDir(), t.TempDir()
	writeSetManifest(t, projA, "alpha", "2026-09-21T10:00:00Z", "Alpha feature", 2)
	writeSetManifest(t, projA, "beta", "2026-09-21T11:00:00Z", "Beta feature", 1)
	writeSetManifest(t, projB, "alpha", "2026-09-21T09:00:00Z", "Duplicate slug", 1)

	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      "tok",
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
		WebAPIProjects: func() []mockups.Project {
			return []mockups.Project{{Name: "projA", Dir: projA}, {Name: "projB", Dir: projB}}
		},
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
			return addr, projA, projB
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatalf("daemon did not accept connections on %s within 2s", addr)
	return addr, projA, projB
}

// writeSetManifest lays down one mockups/<slug>/ tree with n option HTML
// files and a manifest pointing at them.
func writeSetManifest(t *testing.T, projectDir, slug, created, feature string, n int) {
	t.Helper()
	setDir := filepath.Join(projectDir, "mockups", slug)
	require.NoError(t, os.MkdirAll(setDir, 0o750))
	options := make([]map[string]any, n)
	for i := range options {
		file := fmt.Sprintf("%02d-opt.html", i+1)
		require.NoError(t, os.WriteFile(filepath.Join(setDir, file), []byte("<html>"+slug+"</html>"), 0o600))
		options[i] = map[string]any{"n": i + 1, "name": fmt.Sprintf("Option %d", i+1), "file": file}
	}
	manifest := map[string]any{"slug": slug, "feature": feature, "created": created, "options": options}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(setDir, "index.json"), data, 0o600))
}

// writeTwinSetManifest writes a 3b-form set: each option is an HTML file plus
// a component twin, and the manifest lists both ("file" + "component").
func writeTwinSetManifest(t *testing.T, projectDir, slug, created, feature string, n int) {
	t.Helper()
	setDir := filepath.Join(projectDir, "mockups", slug)
	require.NoError(t, os.MkdirAll(setDir, 0o750))
	options := make([]map[string]any, n)
	for i := range options {
		html := fmt.Sprintf("%02d-opt.html", i+1)
		tsx := fmt.Sprintf("%02d-opt.tsx", i+1)
		require.NoError(t, os.WriteFile(filepath.Join(setDir, html), []byte("<html>"+slug+"</html>"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(setDir, tsx), []byte("export default function Opt"+slug+"() { return null }"), 0o600))
		options[i] = map[string]any{"n": i + 1, "name": fmt.Sprintf("Option %d", i+1), "file": html, "component": tsx}
	}
	manifest := map[string]any{"slug": slug, "feature": feature, "created": created, "options": options}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(setDir, "index.json"), data, 0o600))
}

func getJSON(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	res, err := http.Get("http://" + addr + path)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, string(b)
}

func TestWebAPIMockupSets(t *testing.T) {
	addr, _, _ := startMockupAPIServer(t)

	status, body := getJSON(t, addr, "/api/mockup-sets")
	require.Equal(t, http.StatusOK, status, body)

	var sets []struct {
		Slug    string `json:"slug"`
		Feature string `json:"feature"`
		Project string `json:"project"`
		Created string `json:"created"`
		Options []struct {
			N    int    `json:"n"`
			Name string `json:"name"`
			File string `json:"file"`
		} `json:"options"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &sets))
	// Two sets: newest first, and projB's duplicate alpha never surfaces —
	// the slug is the URL key, so first project wins and the dup is dropped.
	require.Len(t, sets, 2)
	assert.Equal(t, "beta", sets[0].Slug)
	assert.Equal(t, "projA", sets[0].Project)
	assert.Equal(t, "alpha", sets[1].Slug)
	assert.Equal(t, "projA", sets[1].Project)
	assert.Equal(t, "2026-09-21T10:00:00Z", sets[1].Created)
	assert.Len(t, sets[1].Options, 2)
	assert.Equal(t, "01-opt.html", sets[1].Options[0].File)
}

func TestWebAPIMockupSetBySlug(t *testing.T) {
	addr, projA, projB := startMockupAPIServer(t)

	status, body := getJSON(t, addr, "/api/mockup-sets/alpha")
	require.Equal(t, http.StatusOK, status, body)
	var got struct {
		Set struct {
			Slug    string                  `json:"slug"`
			Project string                  `json:"project"`
			Options []struct{ File string } `json:"options"`
		} `json:"set"`
		Dir string `json:"dir"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, "alpha", got.Set.Slug)
	assert.Equal(t, "projA", got.Set.Project, "first project wins the slug")
	assert.Len(t, got.Set.Options, 2)
	assert.Equal(t, filepath.Join(projA, "mockups", "alpha"), got.Dir)
	assert.NotEqual(t, filepath.Join(projB, "mockups", "alpha"), got.Dir)

	status, _ = getJSON(t, addr, "/api/mockup-sets/missing-slug")
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = getJSON(t, addr, "/api/mockup-sets/")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestWebAPIMockupSetRoutes_MethodsAndTraversal(t *testing.T) {
	addr, _, _ := startMockupAPIServer(t)

	// POST to the collection: 405, and the body is drained so the connection
	// stays usable (kept alive by default).
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	_, werr := fmt.Fprintf(conn, "POST /api/mockup-sets HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\nhello")
	require.NoError(t, werr)
	res, rerr := http.ReadResponse(reader, nil)
	require.NoError(t, rerr)
	_, _ = io.Copy(io.Discard, res.Body)
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)

	_, werr = conn.Write([]byte("GET /api/mockup-sets/beta HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	require.NoError(t, werr)
	res2, rerr := http.ReadResponse(reader, nil)
	require.NoError(t, rerr)
	b, _ := io.ReadAll(res2.Body)
	assert.Equal(t, http.StatusOK, res2.StatusCode)
	assert.Contains(t, string(b), `"slug":"beta"`)

	// Traversal in the slug is rejected as not-found by the slug vetting.
	for _, p := range []string{"/api/mockup-sets/..%2F..%2Fsecret", "/api/mockup-sets/a%2Fb"} {
		status, _ := getJSON(t, addr, p)
		assert.Equal(t, http.StatusNotFound, status, p)
	}
}

// TestWebAPIMockupSetSource covers GET /api/mockup-sets/{slug}/source/{file}:
// manifest-listed files are served raw (a twin's tsx, or its listed html),
// everything else — unlisted names, nesting, traversal — is 404.
func TestWebAPIMockupSetSource(t *testing.T) {
	proj := t.TempDir()
	writeTwinSetManifest(t, proj, "twins", "2026-09-22T09:00:00Z", "Twin feature", 2)
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr: "127.0.0.1:0", Token: "tok", CmdFactory: echoCmd, Logger: zerolog.Nop(),
		WebAPIProjects: func() []mockups.Project {
			return []mockups.Project{{Name: "p", Dir: proj}}
		},
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()
	t.Cleanup(cancel)
	// Readiness probe: ListenAndServe binds asynchronously; dial until it
	// answers or the first GET races the bind.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if c, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond); derr == nil {
			_ = c.Close()
			break
		}
		require.Less(t, time.Now(), deadline, "web api server never came up")
		time.Sleep(10 * time.Millisecond)
	}

	// A component twin serves as text/plain with the file's contents.
	res, err := http.Get("http://" + addr + "/api/mockup-sets/twins/source/01-opt.tsx")
	require.NoError(t, err)
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", res.Header.Get("Content-Type"))
	assert.Contains(t, string(b), "export default function Opt")

	// A manifest-listed HTML file is served the same way.
	status, body := getJSON(t, addr, "/api/mockup-sets/twins/source/02-opt.html")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "<html>")

	// A file on disk but NOT in the manifest is 404 — the manifest is the
	// contract, the directory is not readable through this route.
	require.NoError(t, os.WriteFile(filepath.Join(proj, "mockups", "twins", "secret.txt"), []byte("nope"), 0o600))
	status, _ = getJSON(t, addr, "/api/mockup-sets/twins/source/secret.txt")
	assert.Equal(t, http.StatusNotFound, status)

	// Traversal and nesting never reach the disk.
	for _, p := range []string{
		"/api/mockup-sets/twins/source/..%2F..%2Fsecret.txt",
		"/api/mockup-sets/twins/source/sub%2F01-opt.tsx",
	} {
		status, _ = getJSON(t, addr, p)
		assert.Equal(t, http.StatusNotFound, status, p)
	}

	// HEAD is 405 — the source route is GET-only like every other /api
	// route — and the connection stays usable afterwards (the 405 must not
	// poison the keep-alive framing).
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	_, werr := fmt.Fprintf(conn, "HEAD /api/mockup-sets/twins/source/01-opt.tsx HTTP/1.1\r\nHost: x\r\n\r\n")
	require.NoError(t, werr)
	res2, rerr := http.ReadResponse(reader, &http.Request{Method: http.MethodHead})
	require.NoError(t, rerr)
	_, _ = io.Copy(io.Discard, res2.Body)
	assert.Equal(t, http.StatusMethodNotAllowed, res2.StatusCode)

	_, werr = conn.Write([]byte("GET /api/mockup-sets/twins/source/01-opt.tsx HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"))
	require.NoError(t, werr)
	res3, rerr := http.ReadResponse(reader, nil)
	require.NoError(t, rerr)
	b3, _ := io.ReadAll(res3.Body)
	assert.Equal(t, http.StatusOK, res3.StatusCode)
	assert.Contains(t, string(b3), "export default function Opt")
}
