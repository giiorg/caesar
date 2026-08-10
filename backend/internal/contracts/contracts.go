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

	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
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

type idAndUpdateProject struct {
	idParams
	assetsapi.UpdateProjectRequest
}

type idAndCreateRepo struct {
	idParams
	assetsapi.CreateRepoRequest
}

type idAndUpdateRepo struct {
	idParams
	assetsapi.UpdateRepoRequest
}

type idAndRotateSecret struct {
	idParams
	assetsapi.RotateCredentialSecretRequest
}

type idAndUpdateHarness struct {
	idParams
	assetsapi.UpdateHarnessRequest
}

type idAndSetGrant struct {
	idParams
	identityapi.SetGrantRequest
}

// grantMemberParams covers DELETE …/{id}/grants/{user_id} — two path
// parameters, no body.
type grantMemberParams struct {
	idParams
	UserID string `path:"user_id" description:"user id (uuid)"`
}

type idAndUpdateWorkflow struct {
	idParams
	workflowsapi.UpdateWorkflowRequest
}

type idAndCreateWorkflowVersion struct {
	idParams
	workflowsapi.CreateWorkflowVersionRequest
}

// workflowVersionParams covers GET …/{id}/versions/{version} — two path
// parameters, no body.
type workflowVersionParams struct {
	idParams
	Version string `path:"version" description:"version number"`
}

// runEventsParams covers GET …/runs/{id}/events — one path parameter
// plus the poll cursor, no body.
type runEventsParams struct {
	idParams
	After string `query:"after" description:"return events with id greater than this"`
}

// gateDecisionParams covers POST …/runs/{id}/gates/{node}/decision — two path
// parameters plus a body.
type gateDecisionParams struct {
	idParams
	Node string `path:"node" description:"gate node id"`
	runsapi.GateDecisionRequest
}

// requestOverrides maps operation IDs whose request combines a body with
// path parameters onto their wrapper structs. Every new id-bearing body
// gets an entry; requestStructure panics loudly otherwise.
var requestOverrides = map[string]any{
	"updateUser":             idAndUpdateUser{},
	"updateProject":          idAndUpdateProject{},
	"createRepo":             idAndCreateRepo{},
	"updateRepo":             idAndUpdateRepo{},
	"rotateCredentialSecret": idAndRotateSecret{},
	"updateHarness":          idAndUpdateHarness{},
	"setGrant":               idAndSetGrant{},
	"deleteGrant":            grantMemberParams{},
	"updateWorkflow":         idAndUpdateWorkflow{},
	"createWorkflowVersion":  idAndCreateWorkflowVersion{},
	"getWorkflowVersion":     workflowVersionParams{},
	"listRunEvents":          runEventsParams{},
	"decideGate":             gateDecisionParams{},
}

// operationTables collects every context's route table with nil
// dependencies — metadata only; handlers never run during generation.
func operationTables() [][]httpx.Operation {
	return [][]httpx.Operation{
		identityapi.Operations(identityapi.New(nil, 0, nil)),
		assetsapi.Operations(assetsapi.New(nil, nil, nil, nil)),
		workflowsapi.Operations(workflowsapi.New(nil, nil)),
		runsapi.Operations(runsapi.New(nil, nil, nil, nil)),
	}
}

// Build reflects every registered operation into an OpenAPI 3.1 document.
func Build() ([]byte, error) {
	r := openapi31.NewReflector()
	r.Spec.Info.WithTitle("caesar API").WithVersion("v1")

	for _, ops := range operationTables() {
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
				oc.AddRespStructure(op.Response,
					openapi.WithHTTPStatus(op.Status),
					openapi.WithContentType(contentTypeOf(op)))
			} else {
				oc.AddRespStructure(nil, openapi.WithHTTPStatus(op.Status))
			}
			oc.AddRespStructure(httpx.ProblemDetails{},
				openapi.WithHTTPStatus(http.StatusBadRequest),
				openapi.WithContentType("application/problem+json"))
			for _, code := range op.Errors {
				oc.AddRespStructure(httpx.ProblemDetails{},
					openapi.WithHTTPStatus(code),
					openapi.WithContentType("application/problem+json"))
			}

			if err := r.AddOperation(oc); err != nil {
				return nil, fmt.Errorf("contracts: add %s: %w", op.Pattern, err)
			}
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
	if o, ok := requestOverrides[op.ID]; ok {
		return o
	}
	hasID := strings.Contains(path, "{id}")
	switch {
	case !hasID:
		return op.Request
	case op.Request == nil:
		return idParams{}
	default:
		panic(fmt.Sprintf("contracts: no path-param wrapper for %s — add one to requestOverrides", op.Pattern))
	}
}

// contentTypeOf is the success response media type. Operations that do not
// declare one answer with JSON, which is every REST endpoint; the streams
// in Plan 10 are the only opt-outs.
func contentTypeOf(op httpx.Operation) string {
	if op.ResponseContentType == "" {
		return "application/json"
	}
	return op.ResponseContentType
}
