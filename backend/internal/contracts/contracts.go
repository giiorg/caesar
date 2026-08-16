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
	"github.com/giiorg/caesar/backend/internal/realtime"
	realtimeapi "github.com/giiorg/caesar/backend/internal/realtime/api"
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

// runEventsParams covers GET …/runs/{id}/events — one path parameter, the
// poll cursor and the stream's resume header, no body. The endpoint answers
// two media types (see altResponses) and each parameter belongs to one of
// them: `after` pages the JSON poll, `Last-Event-ID` resumes the stream.
type runEventsParams struct {
	idParams
	After       string `query:"after" description:"return events with id greater than this"`
	LastEventID string `header:"Last-Event-ID" description:"id of the last frame received; the text/event-stream response resumes above it"`
}

// artifactParams covers GET …/runs/{id}/artifacts/{artifact_id} — two path
// parameters, no body.
//
// Without the entry generation SUCCEEDS and is silently wrong. requestStructure
// panics only where {id} meets a non-nil Request; this operation declares none,
// so it takes the idParams branch and the document loses artifact_id — an
// operation the generated client could not address.
type artifactParams struct {
	idParams
	ArtifactID string `path:"artifact_id" description:"artifact id (uuid)"`
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
	"getArtifact":            artifactParams{},
	"listRunEvents":          runEventsParams{},
	"decideGate":             gateDecisionParams{},
}

// altResponse is a SECOND representation of an operation's success status:
// another media type under the same code, with its own schema.
type altResponse struct {
	Response    any
	ContentType string
}

// altResponses maps operation IDs onto the extra success media types they
// answer with. Today that is one endpoint: GET …/runs/{id}/events serves the
// JSON poll and, on `Accept: text/event-stream`, an SSE stream of frames.
//
// It lives here rather than on httpx.Operation — where route and contract are
// otherwise the same value — for an architectural reason and not a
// convenience: the frame schema is realtime.StreamFrame, and runs/api cannot
// name a realtime type without breaking context isolation (internal/arch,
// CI-failing). contracts is not a bounded context and may see both, so this is
// the only place in the tree that can hold the pairing. It is the same reason
// requestOverrides above exists, and the entry is registered by the same key.
//
// That key is the BARE operation ID, matched against every context's table at
// once, so it assumes IDs are unique process-wide. They are the document's
// operationId and have to be — but nothing here enforces it, and a second table
// declaring "listRunEvents" would silently be given an SSE representation it
// never asked for. requestOverrides has the identical shape and exposure; a
// guard, if one is ever wanted, belongs to both and to Build's loop rather than
// to this table.
var altResponses = map[string][]altResponse{
	"listRunEvents": {{Response: realtime.StreamFrame{}, ContentType: "text/event-stream"}},
}

// operationTables collects every context's route table with nil
// dependencies — metadata only; handlers never run during generation.
func operationTables() [][]httpx.Operation {
	return [][]httpx.Operation{
		identityapi.Operations(identityapi.New(nil, 0, nil)),
		assetsapi.Operations(assetsapi.New(nil, nil, nil, nil)),
		workflowsapi.Operations(workflowsapi.New(nil, nil)),
		runsapi.Operations(runsapi.New(nil, nil, nil, nil, nil)),
		realtimeapi.Operations(realtimeapi.New(nil, nil, nil, nil)),
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
			// A second call on the SAME status ADDS a media type rather than
			// replacing the first — verified against the reflector, and the
			// whole reason content negotiation can be declared at all.
			for _, alt := range altResponses[op.ID] {
				oc.AddRespStructure(alt.Response,
					openapi.WithHTTPStatus(op.Status),
					openapi.WithContentType(alt.ContentType))
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
