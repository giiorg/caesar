package httpx

import (
	"net/http"
	"strings"
)

// SecurityHeaders sets the response headers that make the served UI safe to
// run, and joins the existing CheckOrigin belt rather than replacing it.
//
// style-src carries 'unsafe-inline' and script-src deliberately does not. The
// primitives set inline STYLE ATTRIBUTES for positioning and scroll-lock, which
// style-src governs; refusing them would break every dialog and sheet. Inline
// style is not a script-execution vector, and script-src 'self' is the property
// this policy exists for.
//
// No third-party origins appear anywhere, and that is a consequence of a
// decision made elsewhere: the fonts are self-hosted (they are emitted into
// dist/assets as .woff2 by the bundler), so nothing needs allowing.
func SecurityHeaders(dev bool) Middleware {
	// Written as named locals rather than by index into a slice. The obvious
	// alternative — build the production slice, then reassign directives[1]
	// and directives[5] for dev — is silently wrong the first time anyone
	// inserts a directive: dev then relaxes whatever moved into those slots,
	// and no test that only reads the production policy can see it.
	scriptSrc := "script-src 'self'"
	connectSrc := "connect-src 'self'"
	if dev {
		// Vite's HMR client opens a websocket and injects its own module
		// graph. Relaxed HERE and only here, so production is not shaped by
		// the dev loop's needs.
		scriptSrc = "script-src 'self' 'unsafe-inline' 'unsafe-eval'"
		connectSrc = "connect-src 'self' ws: wss:"
	}
	csp := strings.Join([]string{
		"default-src 'self'",
		scriptSrc,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		connectSrc,
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			if !dev {
				// Browsers ignore HSTS over plain http, so this is inert on a
				// lab origin — but it is still gated, because a developer who
				// once loaded https://localhost would poison every other local
				// service for a year.
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
