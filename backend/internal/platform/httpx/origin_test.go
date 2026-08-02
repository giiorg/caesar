package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

func TestCheckOrigin(t *testing.T) {
	const app = "https://caesar.example.com"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := httpx.CheckOrigin(app)(ok)

	tests := []struct {
		name, method, origin string
		want                 int
	}{
		{"safe method any origin", http.MethodGet, "https://evil.example.com", http.StatusNoContent},
		{"unsafe same origin", http.MethodPost, app, http.StatusNoContent},
		{"unsafe absent origin (curl)", http.MethodPost, "", http.StatusNoContent},
		{"unsafe cross origin", http.MethodPost, "https://evil.example.com", http.StatusForbidden},
		{"delete cross origin", http.MethodDelete, "https://evil.example.com", http.StatusForbidden},
		{"null origin", http.MethodPost, "null", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/api/v1/x", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
