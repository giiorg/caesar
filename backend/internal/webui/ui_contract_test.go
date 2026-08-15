package webui_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/giiorg/caesar/backend/internal/webui"
)

// This file carries NO build tag on purpose.
//
// Everything in it — the routing contract and the two document scanners — is
// logic that needs a bundle-shaped fs.FS, not the real Vite output. Keeping it
// untagged is what puts these guards inside `task backend:test`, and therefore
// inside `task backend:ci`, CI's `backend` job and `scripts/gate`. Under the
// embedui tag ui_embed_test.go runs the SAME contract function against the
// real embedded bundle, so the fixture below is a second subject and never the
// only one: if fstest.MapFS and embed.FS ever disagree about something this
// contract depends on, the embedui lane says so.
//
// What remains embedui-only is only what genuinely requires Vite's output: that
// the generated index.html carries no inline <script>, and that it carries the
// build stamp.

// ---------------------------------------------------------------------------
// The fixture bundle.
// ---------------------------------------------------------------------------

// fixtureAsset is the hashed entry script name. Hardcoded here and NEVER read
// back from the fixture by the contract: the contract discovers it from
// index.html exactly as it must for the real bundle, whose hash changes every
// build.
const fixtureAsset = "assets/index-FIXTUREHASH.js"

// distFixture is a bundle with the same SHAPE as a Vite build: index.html at
// the root, content-hashed files under assets/, and a root-level non-HTML file
// that the SPA fallback must not swallow.
//
// It is deliberately NOT more capable than the real thing. It has no directory
// listing, no case-insensitive lookup and no implicit index resolution, because
// embed.FS has none of those either — a fixture that answered more paths than
// the real bundle would turn "the handler is too permissive" into a green test.
func distFixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<!doctype html><html><head>` +
				`<meta charset="utf-8">` +
				`<link rel="stylesheet" href="/assets/index-FIXTUREHASH.css">` +
				`<script type="module" crossorigin src="/` + fixtureAsset + `"></script>` +
				`</head><body><div id="root"></div></body></html>`)},
		fixtureAsset:                       &fstest.MapFile{Data: []byte("export const x = 1;\n")},
		"assets/index-FIXTUREHASH.css":     &fstest.MapFile{Data: []byte(":root{--x:1}\n")},
		"assets/inter-latin-FIXTURE.woff2": &fstest.MapFile{Data: []byte("not really a font")},
		"manifest.webmanifest":             &fstest.MapFile{Data: []byte(`{"name":"caesar"}`)},
	}
}

// ---------------------------------------------------------------------------
// The routing contract. Run against the fixture here, and against the real
// embedded bundle by ui_embed_test.go.
// ---------------------------------------------------------------------------

// assertBundleRoutingContract is every rule handlerFor enforces that can be
// stated without knowing which bundle it is serving.
//
// Its one precondition on the bundle is the one every real build satisfies:
// index.html names at least one /assets/*.js, and manifest.webmanifest exists
// at the root. Both are asserted rather than assumed, so a bundle that stopped
// satisfying them fails loudly here instead of making the checks vacuous.
func assertBundleRoutingContract(t *testing.T, h http.Handler) {
	t.Helper()

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	t.Run("index is served at the root", func(t *testing.T) {
		rec := get("/")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("GET / Content-Type = %q, want text/html", ct)
		}
	})

	t.Run("unknown paths fall back to the SPA", func(t *testing.T) {
		for _, path := range []string{"/approvals", "/tasks", "/deep/link/that/does/not/exist"} {
			rec := get(path)
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 (SPA fallback)", path, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s Content-Type = %q, want text/html", path, ct)
			}
		}
	})

	t.Run("a miss under /assets is a 404, never index.html", func(t *testing.T) {
		// The fallback must not swallow a genuinely missing asset into
		// index.html: a 200 of HTML where a .js was requested is a broken page
		// with no error.
		for _, path := range []string{
			"/assets/does-not-exist.js",
			"/assets/does-not-exist.css",
			"/assets/nested/does-not-exist.woff2",
			// A double slash reaches this handler unchanged when it is called
			// directly (http.ServeMux would have redirected first), so the
			// assets rule must not be defeated by one.
			"//assets/does-not-exist.js",
			// Same rule from the other direction: a traversal that path.Clean
			// collapses back inside the bundle must not escape the /assets
			// branch into the SPA fallback.
			"/assets/../assets/does-not-exist.js",
		} {
			rec := get(path)
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404 (body: %.80q)", path, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("a real asset is served with its own content type", func(t *testing.T) {
		// The 404 rule above is satisfied by a handler that 404s EVERYTHING
		// under /assets, which would serve a blank page and no error anyone
		// could read. The hashed names change every build, so the asset under
		// test is read out of the served index.html rather than hardcoded.
		body := get("/").Body.String()
		i := strings.Index(body, `src="/assets/`)
		if i < 0 {
			t.Fatalf("index.html names no /assets/ script — this contract has nothing to check:\n%s", body)
		}
		ref := body[i+len(`src="`):]
		ref = ref[:strings.IndexByte(ref, '"')]

		rec := get(ref)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 — index.html names an asset the handler will not serve", ref, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "application/javascript") {
			t.Errorf("GET %s Content-Type = %q, want a JavaScript type — a module served as text/html is refused by the browser", ref, ct)
		}
	})

	t.Run("the assets directory is not listed", func(t *testing.T) {
		// http.FileServer answers a directory with an index listing, which
		// would publish the bundle's file names for nothing.
		for _, path := range []string{"/assets", "/assets/"} {
			rec := get(path)
			if strings.Contains(rec.Body.String(), "<pre>") {
				t.Errorf("GET %s served a directory listing:\n%s", path, rec.Body.String())
			}
		}
	})

	t.Run("root-level bundle files are served as themselves", func(t *testing.T) {
		// The branch neither the /assets rule nor the SPA fallback touches: a
		// real file Vite emits at the root of dist. Without this the branch is
		// unfalsifiable — the SPA fallback answers 200 for every path, so
		// deleting the branch entirely would leave every other check here green
		// while /manifest.webmanifest silently became index.html and the PWA
		// manifest stopped parsing.
		rec := get("/manifest.webmanifest")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /manifest.webmanifest = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET /manifest.webmanifest Content-Type = %q — it was swallowed by the SPA fallback", ct)
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET /manifest.webmanifest served index.html:\n%s", rec.Body.String())
		}
	})
}

func TestRoutingContractHoldsOverAFixtureBundle(t *testing.T) {
	assertBundleRoutingContract(t, webui.HandlerFor(distFixture()))
}

// TestHandlerForRefusesABundleWithNoIndex pins the startup failure. Every path
// through the handler needs index.html, so an absent one has to be a panic at
// construction and not a nil body on some later request.
func TestHandlerForRefusesABundleWithNoIndex(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("handlerFor accepted a bundle with no index.html")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "index.html") {
			t.Errorf("panic value = %v; want a message naming index.html", r)
		}
	}()
	webui.HandlerFor(fstest.MapFS{"assets/x.js": &fstest.MapFile{Data: []byte("x")}})
}

// ---------------------------------------------------------------------------
// The document scanners used by the embedui-only assertions, and their own
// falsification. Pure string functions: they need no bundle, so they run in the
// untagged lane too.
// ---------------------------------------------------------------------------

// isTagSpace reports whether c terminates a tag name in HTML. \r and \f are in
// this set and are NOT decoration: leaving them out is a false-green hole,
// because a `<script\r\n>inline</script>` would then be classified as "not a
// script tag" and skipped by the very check that exists to find it.
func isTagSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// hasSrcAttribute reports whether the attribute text of a <script ...> tag
// carries a real src attribute.
//
// strings.Contains(attrs, "src=") is the obvious version and it is wrong in the
// dangerous direction: `<script data-src="x">console.log(1)</script>` contains
// it, so an inline script would be waved through. An attribute name has to
// start at a tag-space boundary and be followed by '=' — which also rejects
// `srcset=`.
func hasSrcAttribute(attrs string) bool {
	for i := 0; i < len(attrs); i++ {
		if !isTagSpace(attrs[i]) && attrs[i] != '/' {
			continue
		}
		rest := strings.TrimLeft(attrs[i+1:], " \t\n\r\f")
		if !strings.HasPrefix(rest, "src") {
			continue
		}
		rest = strings.TrimLeft(rest[len("src"):], " \t\n\r\f")
		if strings.HasPrefix(rest, "=") {
			return true
		}
	}
	return false
}

// inlineScriptTags returns the attribute text of every <script> tag in doc that
// carries no src, i.e. every tag whose body is inline script.
//
// Stdlib only — golang.org/x/net/html would parse this more properly and is not
// worth a new module dependency for one assertion over a document Vite
// generates.
//
// Substring-matching "<script>" is NOT good enough and would be the more
// dangerous kind of green: Vite emits `<script type="module">`, so the literal
// bare tag this policy forbids in practice never appears.
func inlineScriptTags(t *testing.T, doc string) []string {
	t.Helper()
	var found []string
	for rest := doc; ; {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return found
		}
		rest = rest[i+len("<script"):]
		if rest == "" {
			return found
		}
		// A tag-name boundary. `</script>` never reaches here (it has no
		// "<script" substring), but `<scriptfoo …>` does and is not a script
		// tag. The scan continues from just past the "<script" we consumed, so
		// a rejected candidate never hides a real tag behind it.
		if rest[0] != '>' && rest[0] != '/' && !isTagSpace(rest[0]) {
			continue
		}
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			t.Fatalf("unterminated <script tag in index.html:\n%s", doc)
		}
		attrs := rest[:end]
		if !hasSrcAttribute(attrs) {
			found = append(found, attrs)
		}
		rest = rest[end+1:]
	}
}

// TestScriptTagScannerFindsWhatItMustFind is the scanner's own falsification.
//
// The document under test in the embedui lane is generated and normally
// contains exactly what is expected, so
// TestEmbeddedIndexIsServedAndCarriesNoInlineScript is green in the healthy
// case whether the scanner works or not. These fixtures are the only thing that
// distinguishes "there is no inline script" from "the scanner cannot see one".
func TestScriptTagScannerFindsWhatItMustFind(t *testing.T) {
	inline := []string{
		`<script>console.log(1)</script>`,
		`<script type="module">console.log(1)</script>`,
		"<script\r\n  type=\"module\">console.log(1)</script>",
		`<script data-src="/x.js">console.log(1)</script>`,
		`<script srcset="/x.js">console.log(1)</script>`,
		`<html><head><script nonce="abc">x()</script><script src="/a.js"></script></head></html>`,
	}
	for _, doc := range inline {
		if got := inlineScriptTags(t, doc); len(got) == 0 {
			t.Errorf("scanner missed the inline script in %q", doc)
		}
	}
	clean := []string{
		`<script type="module" crossorigin src="/assets/index-x.js"></script>`,
		`<script src=/a.js></script>`,
		"<script\n  src = \"/a.js\"></script>",
		`<scriptfoo>not a script tag</scriptfoo>`,
		`<html><head></head><body><div id="root"></div></body></html>`,
	}
	for _, doc := range clean {
		if got := inlineScriptTags(t, doc); len(got) != 0 {
			t.Errorf("scanner false-positived on %q: %q", doc, got)
		}
	}
}

var (
	metaTagRe   = regexp.MustCompile(`(?is)<meta\s+([^>]*?)/?>`)
	attributeRe = regexp.MustCompile(`(?is)([a-z][a-z0-9-]*)\s*=\s*"([^"]*)"`)
)

// metaContent returns the content of the first <meta name="..."> whose name
// matches, and whether one was found.
func metaContent(doc, name string) (string, bool) {
	for _, tag := range metaTagRe.FindAllStringSubmatch(doc, -1) {
		attrs := map[string]string{}
		for _, a := range attributeRe.FindAllStringSubmatch(tag[1], -1) {
			attrs[strings.ToLower(a[1])] = a[2]
		}
		if attrs["name"] == name {
			return attrs["content"], true
		}
	}
	return "", false
}

// TestMetaScannerFindsWhatItMustFind falsifies the scanner above. Without it,
// TestServedIndexCarriesTheBuildStamp is green whenever metaContent returns
// something, and cannot distinguish "the stamp is there" from "the scanner
// found the wrong tag".
func TestMetaScannerFindsWhatItMustFind(t *testing.T) {
	const doc = `<head><meta charset="utf-8" />` +
		`<meta name="theme-color" content="#0d1117" />` +
		`<meta name="caesar-commit" content="abc123">` +
		`</head>`
	if got, ok := metaContent(doc, "caesar-commit"); !ok || got != "abc123" {
		t.Errorf("metaContent(caesar-commit) = %q, %v; want %q, true", got, ok, "abc123")
	}
	// The wrong-tag failure mode: a scanner that ignored the name attribute
	// would return "#0d1117" or "utf-8" here and the stamp test would pass on a
	// bundle carrying no stamp at all.
	if got, ok := metaContent(doc, "theme-color"); !ok || got != "#0d1117" {
		t.Errorf("metaContent(theme-color) = %q, %v; want %q, true", got, ok, "#0d1117")
	}
	if got, ok := metaContent(doc, "not-present"); ok {
		t.Errorf("metaContent(not-present) = %q, true; want not found", got)
	}
}
