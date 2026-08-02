package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestWithCorrelationID_GeneratesWhenAbsent(t *testing.T) {
	var seen string
	h := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = CorrelationID(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(seen) {
		t.Fatalf("generated id = %q, want 32 hex chars", seen)
	}
	if got := rec.Header().Get(CorrelationHeader); got != seen {
		t.Fatalf("response header = %q, want %q", got, seen)
	}
}

func TestWithCorrelationID_PropagatesProvidedID(t *testing.T) {
	var seen string
	h := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = CorrelationID(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(CorrelationHeader, "caller-chose-this")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != "caller-chose-this" {
		t.Fatalf("ctx id = %q, want caller's id", seen)
	}
	if got := rec.Header().Get(CorrelationHeader); got != "caller-chose-this" {
		t.Fatalf("response header = %q, want caller's id", got)
	}
}

func TestAccessLog_EmitsMethodPathStatusAndCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
		WithCorrelationID, AccessLog(log),
	)
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set(CorrelationHeader, "cid-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, buf.String())
	}
	if line["method"] != "GET" || line["path"] != "/healthz" || line["status"] != float64(418) || line["correlation_id"] != "cid-1" {
		t.Fatalf("unexpected access log line: %v", line)
	}
}

func TestChain_AppliesFirstMiddlewareOutermost(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), mk("outer"), mk("inner"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if len(order) != 2 || order[0] != "outer" || order[1] != "inner" {
		t.Fatalf("order = %v, want [outer inner]", order)
	}
}
