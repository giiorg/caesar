package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// getHealthzWithPinger is shared with serve_integration_test.go (same package).
func getHealthzWithPinger(t *testing.T, db Pinger) (*http.Response, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(Routes(discard(), db))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestHealthz_ReportsOKWhenDatabaseReachable(t *testing.T) {
	resp, body := getHealthzWithPinger(t, fakePinger{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body["status"] != "ok" || body["db"] != "ok" {
		t.Fatalf("body = %v", body)
	}
	if body["version"] == "" {
		t.Fatal("version missing from healthz body")
	}
}

func TestHealthz_Reports503WhenDatabaseDown(t *testing.T) {
	resp, body := getHealthzWithPinger(t, fakePinger{err: errors.New("conn refused")})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if body["status"] != "degraded" || body["db"] != "unreachable" {
		t.Fatalf("body = %v", body)
	}
}

func TestHealthz_SetsCorrelationIDHeader(t *testing.T) {
	resp, _ := getHealthzWithPinger(t, fakePinger{})
	if resp.Header.Get("X-Correlation-ID") == "" {
		t.Fatal("correlation id header missing")
	}
}
