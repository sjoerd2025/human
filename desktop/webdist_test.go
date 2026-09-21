//go:build wailsapp

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxTestMux wires the middleware exactly as main.go does: the sandbox
// middleware in front of mockupMiddleware and a marker handler standing in for
// everything that is neither /mocks/ nor /mockups/.
func sandboxTestMux(t *testing.T) (*http.ServeMux, *bool) {
	t.Helper()
	mux := http.NewServeMux()
	passed := false
	// main.go composes: sandboxMiddleware(mockupMiddleware(inner-marker)). The
	// marker answers what neither the sandbox nor the disk mockups own.
	mux.Handle("/", sandboxMiddleware(mockupMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
	}))))
	return mux, &passed
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSandboxServesIndexAtRoot(t *testing.T) {
	mux, _ := sandboxTestMux(t)
	rec := get(t, mux, "/mocks/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mocks/ = %d, want 200 (index.html)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div id=\"root\"") {
		t.Errorf("GET /mocks/ body is not the sandbox index.html")
	}
}

func TestSandboxSPAFallbackForRoutes(t *testing.T) {
	mux, _ := sandboxTestMux(t)
	// The embed bridge and any extension-less app route fall back to the
	// shell — that is what makes /mocks/embed/mockups/<slug>/<file> work.
	for _, path := range []string{"/mocks/embed/mockups/set1/01-a.html", "/mocks/preview/X", "/mocks"} {
		rec := get(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (SPA fallback)", path, rec.Code)
		}
	}
}

func TestSandboxMissingAssetIsLoud404(t *testing.T) {
	mux, _ := sandboxTestMux(t)
	rec := get(t, mux, "/mocks/assets/nope-does-not-exist.js")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing asset = %d, want 404 (never the shell)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<div id=\"root\"") {
		t.Errorf("missing asset served index.html — SPA fallback must not hide broken assets")
	}
}

func TestSandboxPassesMockupsThrough(t *testing.T) {
	mux, _ := sandboxTestMux(t)
	// The invariant that matters: the sandbox mount never answers paths it
	// does not own. /mockups/… is mockupMiddleware's prefix (it may 404 an
	// unknown slug itself — that is its call); /other belongs to the board's
	// asset server. Neither may surface the sandbox shell.
	for _, path := range []string{"/mockups/set1/01-a.html", "/mockups/", "/other"} {
		rec := get(t, mux, path)
		if strings.Contains(rec.Body.String(), "<div id=\"root\"") {
			t.Errorf("GET %s was answered by the sandbox; sandboxMiddleware must not own this path", path)
		}
	}
}

func TestSandboxBridgesInnerMockupsHop(t *testing.T) {
	// The bridge's inner hop: the sandbox's EmbedFrame frames the disk-served
	// set resolved against the sandbox's base path, so the request arrives as
	// /mocks/mockups/<slug>/<file> and must be rebased onto /mockups/ and
	// handed to mockupMiddleware. Without the rebase the extension rule
	// serves it from the embedded FS — always a 404, i.e. every option in
	// the Mockups view shows the webview's error page instead of the mockup.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01-a.html"), []byte("SET-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	mockupMu.Lock()
	mockupDirs["set1"] = dir
	mockupMu.Unlock()

	h := sandboxMiddleware(mockupMiddleware(http.NotFoundHandler()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mocks/mockups/set1/01-a.html", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "SET-CONTENT" {
		t.Fatalf("GET /mocks/mockups/set1/01-a.html = %d %q, want 200 with the disk-served set file", rec.Code, rec.Body.String())
	}
}

func TestSandboxEmbedBridgeRejectsTraversal(t *testing.T) {
	// Invoked directly (not through http.ServeMux, which cleans dot segments
	// and redirects before the middleware would see them) so THIS layer's
	// guard is what is under test.
	passed := false
	h := sandboxMiddleware(mockupMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
	})))
	for _, path := range []string{"/mocks/embed/../secret", "/mocks/embed//etc/passwd"} {
		passed = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if passed {
			t.Errorf("GET %s fell through to the next handler instead of being rejected", path)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (traversal rejected)", path, rec.Code)
		}
	}
}
