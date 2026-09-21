//go:build wailsapp

package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The React sandbox (web/artifacts/mockup-sandbox) is built separately from
// the board and copied into web-dist/ (make web-dist-check refreshes and
// guards it — desktop/web-dist is a committed artifact whose drift against a
// fresh build is exactly what the guard exists to catch, SC-3613 for the
// pnpm side). It must be built with BASE_PATH=/mocks/ PORT=5174: a
// default-baked build references its assets at /assets/… and 404s every one
// of them under this mount.
//
//go:embed all:web-dist
var webDist embed.FS

// sandboxMiddleware serves the embedded sandbox at /mocks/. It is composed
// in main.go BEFORE mockupMiddleware (which serves the disk-based mockup
// sets at the distinct /mockups/ prefix) so both mounts stay reachable:
//
//	/mocks/            → the sandbox app (index.html)
//	/mocks/assets/…    → hashed bundle assets, 404 when missing
//	/mocks/embed/<t>   → the 3a bridge: the sandbox iframes <t> resolved
//	                     against its base path (e.g. mockups/<slug>/<file>)
//	/mocks/mockups/…   → the bridge's inner hop, rebased onto /mockups/ and
//	                     handed to mockupMiddleware (disk-served sets)
//	/mockups/<slug>/…  → (mockupMiddleware) static sets from disk
func sandboxMiddleware(next http.Handler) http.Handler {
	sub, err := fs.Sub(webDist, "web-dist")
	if err != nil {
		// The embed directive guarantees web-dist exists; this is unreachable
		// for a valid build. Panic rather than serve a half-mounted app.
		panic("desktop: embedded web-dist is not a readable filesystem: " + err.Error())
	}
	fileServer := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/mocks/")
		if !ok {
			// "/mocks" without the trailing slash and every other path (in
			// particular /mockups/…, owned by mockupMiddleware) flow through.
			if r.URL.Path != "/mocks" {
				next.ServeHTTP(w, r)
				return
			}
			rest = ""
		}

		if rest == "" {
			serveSandboxIndex(w, r, sub)
			return
		}

		// The 3a bridge: /mocks/embed/<target> frames <target> from the
		// sandbox itself, keeping the board one hop from any disk-served set.
		if target, ok := strings.CutPrefix(rest, "embed/"); ok && target != "" {
			// The iframe URL is board-controlled (mockupsview.ts); keep it a
			// relative, traversal-free path before handing it to the app.
			if strings.Contains(target, "..") || strings.HasPrefix(target, "/") {
				http.NotFound(w, r)
				return
			}
			serveSandboxIndex(w, r, sub)
			return
		}

		// The 3a bridge's inner hop: EmbedFrame (the sandbox's /embed/ route)
		// frames the disk-served set resolved against the sandbox's base path,
		// so the request lands here as /mocks/mockups/<slug>/<file>. Without
		// this hand-off the extension rule below serves it from the embedded
		// FS — always a 404, i.e. every option in the Mockups view renders the
		// webview's error page instead of the mockup. mockupMiddleware only
		// answers the /mockups/ prefix, so rebase the path and pass it on.
		if setPath, ok := strings.CutPrefix(rest, "mockups/"); ok && !strings.Contains(setPath, "..") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/mockups/" + setPath
			next.ServeHTTP(w, r2)
			return
		}

		// Extension-carrying paths are real files: serve exactly, 404 loudly
		// when absent (a missing hashed asset must never masquerade as the
		// app shell). Extension-less paths are app routes: SPA fallback.
		rel := path.Clean("/" + rest)
		if path.Base(rel) != "" && strings.Contains(path.Base(rel), ".") {
			http.StripPrefix("/mocks/", fileServer).ServeHTTP(w, r)
			return
		}
		serveSandboxIndex(w, r, sub)
	})
}

// serveSandboxIndex responds with the sandbox's index.html, or 404 when the
// embedded tree is missing it (an empty or non-sandbox web-dist).
func serveSandboxIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
