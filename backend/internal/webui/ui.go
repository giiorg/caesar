// Package webui serves the built web client. Which of its two Handler
// constructors compiles is decided by the embedui build tag: the embedded
// bundle (ui_embed.go), or the honest refusal (ui_noembed.go).
//
// The SERVING RULES live in this file, which carries no build tag, and that
// split is deliberate. The only thing the tag can decide is where the bytes
// come from; the routing contract — SPA fallback, the /assets exception, no
// directory listings — is ordinary logic over an fs.FS and there is no reason
// for it to be invisible to `go vet`, `golangci-lint`, the house analyzers and
// `task backend:test`. Before this split it WAS invisible to all four:
// `go list -f '{{.GoFiles}} {{.IgnoredGoFiles}}' ./internal/webui/` reported
// GoFiles=[ui_noembed.go] with everything else ignored, so exactly one of the
// package's ten tests ran in any automated lane.
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// assetsDir is the one prefix under which a miss is a 404 rather than the SPA
// fallback. Named once because three separate decisions below depend on it
// meaning exactly the same thing.
const assetsDir = "assets"

// handlerFor serves bundle and falls back to its index.html for unknown paths,
// so a deep link into a client route reloads correctly.
//
// bundle is the ROOT of the built client: index.html at bundle's top level,
// hashed files under bundle's "assets/". Under the embedui tag that is
// fs.Sub(dist, "dist"); in tests it is a fixture with the same shape.
//
// It panics when index.html is absent, because every path this handler can
// take needs it and a nil index would turn one broken build into a nil-deref
// on some later request instead of a startup failure.
//
// The fallback is deliberately NOT applied under /assets: those names are
// content-hashed, so a miss there is a broken build, and answering it with
// HTML would turn a 404 anyone can diagnose into a script that silently parses
// as a document.
//
// Three properties are load-bearing and each was MEASURED against a version
// that lacked it:
//
//  1. The request path is normalised with path.Clean before anything reads it.
//     Without that, `GET //assets/does-not-exist.js` answered 200 text/html —
//     a single extra slash defeated the whole /assets rule, because a
//     TrimPrefix of one "/" leaves "/assets/…", which does not carry the
//     "assets/" prefix and is not a valid fs path either. Cleaning also
//     collapses any "..", so nothing can address outside the bundle.
//
//  2. Directories are never served. http.FileServerFS answers a directory with
//     an index listing; `GET /assets/` published every hashed filename in the
//     bundle, fonts included. A directory is now indistinguishable from a
//     missing file.
//
//  3. The dispatch is a flat sequence of returns rather than an if/else-if
//     pair. The else-if arrangement made the empty-path case and the
//     assets-prefix case share a branch, which is what hid (1).
func handlerFor(bundle fs.FS) http.Handler {
	index, err := fs.ReadFile(bundle, "index.html")
	if err != nil {
		panic("webui: bundle has no index.html: " + err.Error())
	}

	// isFile reports whether name is a regular file inside the bundle. A
	// directory answers false: see property (2) above.
	isFile := func(name string) bool {
		info, err := fs.Stat(bundle, name)
		return err == nil && !info.IsDir()
	}

	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path.Clean always returns a rooted, slash-collapsed, dot-free path
		// for a "/"-prefixed input, so name is a valid io/fs name and "" for
		// the site root.
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

		// Under /assets the answer is the file or a 404 — never index.html.
		// The bare "/assets" and "/assets/" forms land here too, and are a
		// miss, because assetsDir is a directory and isFile says no.
		if name == assetsDir || strings.HasPrefix(name, assetsDir+"/") {
			if !isFile(name) {
				http.NotFound(w, r)
				return
			}
			// name, not r.URL.Path: this is the normalised spelling, and it is
			// the one that was just Stat'ed. Handing the raw request path to a
			// FileServer instead is how the double-slash hole opened.
			http.ServeFileFS(w, r, bundle, name)
			return
		}

		// Anything else that really exists in the bundle is served as itself:
		// /manifest.webmanifest, /favicon.ico, /index.html.
		if name != "" && isFile(name) {
			http.ServeFileFS(w, r, bundle, name)
			return
		}

		// Everything else is a client route. The SPA owns it.
		serveIndex(w)
	})
}
