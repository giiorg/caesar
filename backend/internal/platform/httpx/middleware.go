// Package httpx holds caesar's HTTP middleware primitives. Correlation IDs
// ride every request (spec §4): propagated from X-Correlation-ID or
// generated, echoed in the response, stamped into every access-log line.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type Middleware func(http.Handler) http.Handler

// Chain wraps h so that the first middleware is outermost.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

const CorrelationHeader = "X-Correlation-ID"

type ctxKey int

const correlationKey ctxKey = iota

// CorrelationID returns the request's correlation id, or "" outside a request.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
}

func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if id == "" {
			id = newID()
		}
		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, id)))
	})
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("httpx: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// AccessLog logs one line per request. It must sit inside WithCorrelationID.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("correlation_id", CorrelationID(r.Context())),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush/Hijack on the underlying
// writer — required for SSE streaming later (spec §4).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
