package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Pinger is the slice of the pgx pool the HTTP layer needs.
type Pinger interface {
	Ping(context.Context) error
}

// Routes assembles caesar's HTTP surface: health, the mounted /api/v1
// operations, a problem+json 404 for unknown API paths, and the web client at
// everything else.
//
// The UI handler is passed IN rather than constructed here so that internal/app
// does not have to care which twin of internal/webui compiled — and so the
// tests can mount a recogniser instead of a bundle.
func Routes(log *slog.Logger, db Pinger, ops []httpx.Operation, appOrigin string, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealthz(db))
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such endpoint"))
	}))
	httpx.Mount(mux, ops)
	// Registered LAST and at the root, so every /api/ path and /healthz keeps
	// its own handler: ServeMux prefers the more specific pattern, and the
	// /api/ catch-all above is what keeps an unknown API path a typed
	// problem+json rather than an HTML page a client cannot parse.
	//
	// The method guard is not decoration. A ServeMux pattern with no method
	// matches EVERY method, so a bare `mux.Handle("/", ui)` also claims
	// POST/PUT/DELETE /healthz — which the "GET /healthz" pattern used to
	// answer 405. MEASURED before the guard: POST /healthz returned 200
	// "<html>ui</html>" with the database down, i.e. a probe using any method
	// but GET reported a dead process healthy.
	mux.Handle("/", uiOnlyReads(ui))
	return httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log),
		httpx.CheckOrigin(appOrigin), httpx.SecurityHeaders(false))
}

// uiOnlyReads narrows the root mount to the methods a bundle of static files
// can answer, restoring the 405 that the pattern set produced before the UI
// existed.
//
// The obvious alternative — registering the pattern as "GET /" so ServeMux
// does this itself — does not compile into a working mux. MEASURED: it panics
// at registration, because Go refuses a pattern that matches fewer methods than
// an existing one while having a more general path:
//
//	pattern "GET /" … conflicts with pattern "/api/": GET / matches fewer
//	methods than /api/, but has a more general path pattern
//
// The refusal is deliberately byte-identical to the one net/http writes for a
// method mismatch (Allow, then a text/plain "Method Not Allowed"), rather than
// a problem+json: this path is reached by browsers and probes aimed at the web
// client, and matching the stdlib keeps the pre-UI behaviour of /healthz
// unchanged rather than merely similar.
func uiOnlyReads(ui http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HEAD is served: ServeMux treats it as GET, static file servers answer
		// it natively, and uptime probes use it.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		ui.ServeHTTP(w, r)
	})
}

func handleHealthz(db Pinger) http.Handler {
	type response struct {
		Status  string `json:"status"`
		DB      string `json:"db"`
		Version string `json:"version"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		resp := response{Status: "ok", DB: "ok", Version: buildinfo.Version}
		code := http.StatusOK
		if err := db.Ping(ctx); err != nil {
			resp = response{Status: "degraded", DB: "unreachable", Version: buildinfo.Version}
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
