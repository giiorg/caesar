package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func problemFor(t *testing.T, err error) (int, httpx.ProblemDetails, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
	// Run through the correlation middleware so the id is in context.
	httpx.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, err)
	})).ServeHTTP(rec, req)
	var pd httpx.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	return rec.Code, pd, rec.Header()
}

func TestProblem_MapsClassesToStatuses(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{"invalid", xerrors.E(xerrors.ClassInvalid, "bad input"), http.StatusBadRequest, "/errors/invalid"},
		{"unauthenticated", xerrors.E(xerrors.ClassUnauthenticated, "who are you"), http.StatusUnauthorized, "/errors/unauthenticated"},
		{"forbidden", xerrors.E(xerrors.ClassForbidden, "no"), http.StatusForbidden, "/errors/forbidden"},
		{"not found", xerrors.E(xerrors.ClassNotFound, "gone"), http.StatusNotFound, "/errors/not-found"},
		{"conflict", xerrors.E(xerrors.ClassConflict, "clash"), http.StatusConflict, "/errors/conflict"},
		{"rate limited", xerrors.E(xerrors.ClassRateLimited, "slow down"), http.StatusTooManyRequests, "/errors/rate-limited"},
		{"internal", errors.New("pq: connection refused"), http.StatusInternalServerError, "/errors/internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, pd, hdr := problemFor(t, tt.err)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if pd.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", pd.Type, tt.wantType)
			}
			if pd.Status != tt.wantStatus {
				t.Fatalf("body status = %d, want %d", pd.Status, tt.wantStatus)
			}
			if got := hdr.Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q", got)
			}
			if pd.CorrelationID == "" {
				t.Fatal("correlation_id must be present")
			}
		})
	}
}

func TestProblem_NeverLeaksInternalDetail(t *testing.T) {
	_, pd, _ := problemFor(t, errors.New("pq: password authentication failed for user caesar"))
	if pd.Detail != "internal error" {
		t.Fatalf("internal detail leaked: %q", pd.Detail)
	}
}

func TestProblem_CarriesFieldErrors(t *testing.T) {
	err := xerrors.E(xerrors.ClassInvalid, "validation failed").
		WithFields(xerrors.FieldError{Field: "email", Message: "not a valid address"})
	_, pd, _ := problemFor(t, err)
	want := []xerrors.FieldError{{Field: "email", Message: "not a valid address"}}
	if diff := cmp.Diff(want, pd.Errors); diff != "" {
		t.Fatalf("errors mismatch (-want +got):\n%s", diff)
	}
	if pd.Detail != "validation failed" {
		t.Fatalf("detail = %q", pd.Detail)
	}
}
