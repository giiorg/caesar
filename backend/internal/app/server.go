package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

// Pinger is the slice of the pgx pool the HTTP layer needs.
type Pinger interface {
	Ping(context.Context) error
}

// Routes assembles caesar's HTTP surface. Plan 2 mounts /api/v1 here.
func Routes(log *slog.Logger, db Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealthz(db))
	return httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log))
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
