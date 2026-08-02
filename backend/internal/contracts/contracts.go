// Package contracts turns the httpx.Operation route tables into the
// committed OpenAPI 3.1 document (spec §4: Go types are the source of
// truth; CI gates regen-diff). It is tooling, not a bounded context —
// internal/arch and txboundary both list it as nonContext.
package contracts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi31"

	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

// idParams declares the {id} path parameter for operations whose pattern
// carries one (the stdlib pattern and the OpenAPI template share syntax).
type idParams struct {
	ID string `path:"id" description:"resource id (uuid)"`
}

type idAndUpdateUser struct {
	idParams
	identityapi.UpdateUserRequest
}

// Build reflects every registered operation into an OpenAPI 3.1 document.
func Build() ([]byte, error) {
	r := openapi31.NewReflector()
	r.Spec.Info.WithTitle("caesar API").WithVersion("v1")

	ops := identityapi.Operations(identityapi.New(nil, 0)) // metadata only; handlers never run
	for _, op := range ops {
		method, path, ok := strings.Cut(op.Pattern, " ")
		if !ok {
			return nil, fmt.Errorf("contracts: pattern %q has no method", op.Pattern)
		}
		oc, err := r.NewOperationContext(method, path)
		if err != nil {
			return nil, fmt.Errorf("contracts: %s: %w", op.Pattern, err)
		}
		oc.SetID(op.ID)
		oc.SetSummary(op.Summary)
		oc.SetDescription("Errors use the RFC 9457 problem+json envelope on every non-2xx status.")

		if req := requestStructure(op, path); req != nil {
			oc.AddReqStructure(req)
		}
		if op.Response != nil {
			oc.AddRespStructure(op.Response, openapi.WithHTTPStatus(op.Status))
		} else {
			oc.AddRespStructure(nil, openapi.WithHTTPStatus(op.Status))
		}
		oc.AddRespStructure(httpx.ProblemDetails{},
			openapi.WithHTTPStatus(http.StatusBadRequest),
			openapi.WithContentType("application/problem+json"))

		if err := r.AddOperation(oc); err != nil {
			return nil, fmt.Errorf("contracts: add %s: %w", op.Pattern, err)
		}
	}

	raw, err := json.MarshalIndent(r.Spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("contracts: marshal: %w", err)
	}
	return append(raw, '\n'), nil
}

// requestStructure folds the {id} path parameter into the request shape.
func requestStructure(op httpx.Operation, path string) any {
	hasID := strings.Contains(path, "{id}")
	switch {
	case !hasID:
		return op.Request
	case op.Request == nil:
		return idParams{}
	default:
		// Only updateUser combines a body with {id} today; extend this
		// switch when new id-bearing bodies appear.
		if _, ok := op.Request.(identityapi.UpdateUserRequest); ok {
			return idAndUpdateUser{}
		}
		panic(fmt.Sprintf("contracts: no path-param wrapper for %s — add one", op.Pattern))
	}
}
