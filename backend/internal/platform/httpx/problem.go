package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// ProblemDetails is caesar's RFC 9457 envelope — the single error shape
// every /api/v1 endpoint returns (spec §4).
type ProblemDetails struct {
	Type          string               `json:"type"`
	Title         string               `json:"title"`
	Status        int                  `json:"status"`
	Detail        string               `json:"detail,omitempty"`
	CorrelationID string               `json:"correlation_id,omitempty"`
	Errors        []xerrors.FieldError `json:"errors,omitempty"`
}

type problemShape struct {
	status int
	slug   string
	title  string
}

var problemShapes = map[xerrors.Class]problemShape{
	xerrors.ClassInvalid:         {http.StatusBadRequest, "invalid", "Invalid request"},
	xerrors.ClassUnauthenticated: {http.StatusUnauthorized, "unauthenticated", "Authentication required"},
	xerrors.ClassForbidden:       {http.StatusForbidden, "forbidden", "Forbidden"},
	xerrors.ClassNotFound:        {http.StatusNotFound, "not-found", "Not found"},
	xerrors.ClassConflict:        {http.StatusConflict, "conflict", "Conflict"},
	xerrors.ClassRateLimited:     {http.StatusTooManyRequests, "rate-limited", "Rate limited"},
	xerrors.ClassInternal:        {http.StatusInternalServerError, "internal", "Internal error"},
}

// Problem writes err as a problem+json response. Unclassified errors are
// internal by default and their detail is never leaked (default-deny).
func Problem(w http.ResponseWriter, r *http.Request, err error) {
	class := xerrors.ClassOf(err)
	shape := problemShapes[class]

	pd := ProblemDetails{
		Type:          "/errors/" + shape.slug,
		Title:         shape.title,
		Status:        shape.status,
		Detail:        "internal error",
		CorrelationID: CorrelationID(r.Context()),
	}
	var xe *xerrors.Error
	if class != xerrors.ClassInternal && errors.As(err, &xe) {
		pd.Detail = xe.Msg
		pd.Errors = xe.Fields
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(shape.status)
	_ = json.NewEncoder(w).Encode(pd)
}

// JSON writes the success-path response body.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
