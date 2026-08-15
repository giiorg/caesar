//go:build embedui

package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

// dist is the Vite build, written straight here by web/vite.config.ts's
// build.outDir. One artifact, one location, no copy step to go stale.
//
// dist is gitignored, so this file only compiles after a `task web:build`.
// That is the intended trade: the embedui lane is the one that needs a bundle,
// and the tagless twin is what keeps `task backend:test` Node-free.
//
// THE COST OF THAT TRADE, stated here because it is invisible from the file
// itself: no UNTAGGED lane compiles this file, so no untagged lane checks it.
// MEASURED — `go list -f "<.GoFiles> <.IgnoredGoFiles>" ./internal/webui/`
// reports this file and ui_embed_test.go under IgnoredGoFiles, and
// backend:vet, backend:lint, backend:analyze and backend:vuln all run with
// -tags integration,property,runnerintegration only. `task
// backend:static:embedui` is the lane that compiles it, and `task
// backend:test:embedui` runs its tests; neither is reached by backend:ci or
// scripts/gate yet — §14 owns that wiring.
//
// That cost is why this file is now three statements long. Everything that can
// be checked without a bundle — the whole routing contract — was moved to the
// untagged ui.go, so what the analysers cannot see is the //go:embed
// directive, the fs.Sub, and nothing else.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the embedded bundle. The serving rules are handlerFor's, in
// ui.go; this constructor's only job is to say which bytes.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: dist is not embedded: " + err.Error())
	}
	return handlerFor(sub)
}
