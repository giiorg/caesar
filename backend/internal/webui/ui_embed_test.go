//go:build embedui

package webui_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/webui"
)

// What is left in this file is exactly what CANNOT be checked without Vite's
// real output. Everything else — the routing contract and both document
// scanners — moved to the untagged ui_contract_test.go so that it runs in
// `task backend:test`. See that file's header.

// TestTheRealBundleSatisfiesTheRoutingContract runs the same contract the
// fixture runs, against the embedded bundle.
//
// It is the answer to "the fixture is a fake": fstest.MapFS and embed.FS are
// different implementations of fs.FS, and this is where a disagreement between
// them about Stat-on-a-directory, Sub, or ServeFileFS's seeking shows up. If
// this ever fails while TestRoutingContractHoldsOverAFixtureBundle passes, the
// fixture is the thing that is wrong.
func TestTheRealBundleSatisfiesTheRoutingContract(t *testing.T) {
	assertBundleRoutingContract(t, webui.Handler())
}

func TestEmbeddedIndexIsServedAndCarriesNoInlineScript(t *testing.T) {
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Positive control: an index.html that somehow contained no <script at all
	// would satisfy the inline assertion vacuously, and would also be a broken
	// bundle nobody would notice from this test.
	if !strings.Contains(body, "<script") {
		t.Fatalf("index.html has no <script tag at all — the bundle is not wired in:\n%s", body)
	}
	// ASSERTED, NOT REASONED ABOUT. Vite's module-preload polyfill is often
	// assumed to break script-src 'self'; it does not — it is injected as an
	// import into the entry's proxy module and lands in bundled application
	// JavaScript the policy already permits. That is a reason not to
	// pre-emptively disable it and NO reason at all to trust the outcome: this
	// assertion holds whatever any given Vite version chooses to emit, and the
	// reasoning does not. A hash-based script-src is the fallback if some
	// inline emission ever does survive.
	//
	// HOW TO FALSIFY THIS TEST — MEASURED 2026-08-15, and NOT what the plan
	// says. Adding `<script type="module">console.log(1)</script>` to
	// web/index.html and rebuilding leaves this test GREEN: Vite treats an
	// inline module script as another entry, compiles it into the bundle, and
	// emits an index.html with no inline tag at all. That mutant proves
	// nothing.
	//
	// The mutant that DOES make this fail is a CLASSIC inline script, which
	// Vite passes through untouched:
	//
	//	<script>document.documentElement.dataset.theme=localStorage.theme</script>
	//
	// That is also the realistic threat rather than a contrived one — it is
	// verbatim the shape a reviewer proposed for stamping the theme before
	// first paint, and the reason theme-dark.css carries a note not to re-add
	// it. Rebuild and this test reports:
	//
	//	index.html contains an inline <script>, which script-src 'self' forbids …
	//
	// Note also that the plan's own falsification command passes
	// `-run TestIndexHasNoInlineScript`, which matches no function here; go
	// test then prints "no tests to run" and exits 0. Use the real name.
	for _, attrs := range inlineScriptTags(t, body) {
		t.Errorf("index.html contains an inline <script%s>, which script-src 'self' forbids whatever its type attribute says:\n%s", attrs, body)
	}
}

// TestServedIndexCarriesTheBuildStamp guards the oracle §8's image assertion
// reads: a container is asked for its own index.html and the stamp in it is
// compared against the SHA the release workflow holds independently, so a
// stale-but-self-consistent bundle cannot grade itself as fresh.
//
// It exists because the first version of that mechanism did not work at all,
// and nothing went red. MEASURED 2026-08-15: vite.config.ts declared
// `define: { __CAESAR_COMMIT__: … }`, but `__CAESAR_COMMIT__` was referenced by
// no emitted source — only declared in web/src/vite-env.d.ts — so the
// substitution ran at zero call sites. `CAESAR_COMMIT=DEADBEEFCAFE0001 vite
// build` produced a dist in which `grep -rl DEADBEEFCAFE0001` exited 1 across
// every file, while the Dockerfile's comment claimed the stamp was baked in.
// The fix is vite.config.ts's caesar-commit-stamp plugin; this is its guard.
//
// When CAESAR_COMMIT is set in the environment the assertion is exact, which is
// what makes it an oracle rather than a presence check: the value the build was
// GIVEN must be the value the bundle CARRIES. `task web:build` sets nothing, so
// a local run asserts the weaker "there is a stamp and it is not the
// unsubstituted placeholder".
func TestServedIndexCarriesTheBuildStamp(t *testing.T) {
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	got, ok := metaContent(body, "caesar-commit")
	if !ok {
		t.Fatalf("served index.html carries no <meta name=\"caesar-commit\">; §8's image assertion has nothing to read:\n%s", body)
	}
	if got == "" {
		t.Errorf("the caesar-commit stamp is empty")
	}
	if strings.Contains(got, "__CAESAR_COMMIT__") {
		t.Errorf("the caesar-commit stamp is the unsubstituted placeholder %q", got)
	}
	if want := os.Getenv("CAESAR_COMMIT"); want != "" && got != want {
		t.Errorf("caesar-commit stamp = %q, want %q — the bundle under test was not built from this commit", got, want)
	}
}
