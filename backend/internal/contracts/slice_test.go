package contracts

import (
	"encoding/json"
	"testing"
)

// TestSliceEndpointsDocumentTheirRealAnswers pins what Plan 11's web slice
// consumes. The client reads the STATUS, not the generated union, so these
// entries are documentation of what the handlers already do — but a login body
// whose email and password are both optional is not a contract the form layer
// can lean on, and openapi-typescript renders an unmarked property optional.
func TestSliceEndpointsDocumentTheirRealAnswers(t *testing.T) {
	raw, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	// ONLY statuses the handlers actually produce. 403 was listed here for
	// /approvals and /stream on the assumption that an unauthorized caller is
	// refused; neither handler does that. handleListApprovals SCOPES to the
	// grants the caller holds and answers 200 with an empty list when there
	// are none (runs/api/api.go), and the stream resolves a possibly-empty
	// scope the same way — its `forbidden` is a terminal FRAME over a 200 that
	// was already committed, not an HTTP status. Documenting a status the
	// server cannot send teaches the client to write a branch that never runs,
	// which is worse than silence.
	wantStatuses := map[string]map[string][]string{
		"/api/v1/auth/login": {"post": {"200", "400", "401", "429"}},
		"/api/v1/auth/me":    {"get": {"200", "400", "401"}},
		"/api/v1/approvals":  {"get": {"200", "400", "401"}},
		"/api/v1/stream":     {"get": {"200", "400", "401"}},
	}
	for path, methods := range wantStatuses {
		for method, codes := range methods {
			op, ok := doc.Paths[path][method]
			if !ok {
				t.Fatalf("%s %s is not in the document", method, path)
			}
			for _, code := range codes {
				if _, ok := op.Responses[code]; !ok {
					t.Errorf("%s %s: no %s response documented", method, path, code)
				}
			}
		}
	}

	wantRequired := map[string][]string{
		"ApiLoginRequest":         {"email", "password"},
		"ApiMeResponse":           {"user", "grants"},
		"ApiUserResponse":         {"id", "email", "role", "active", "created_at", "updated_at"},
		"ApiGrantResponse":        {"project_id", "level"},
		"ApiApprovalListResponse": {"approvals"},
		"ApiApprovalResponse":     {"run_id", "task_id", "task_title", "node_id", "project_id", "requester_id", "attempt", "generation", "waiting_since", "evidence"},
		// Every gate the approvals inbox lists is one awaiting a decision, so
		// each of these is a value the server always sends. summary_artifact_id
		// is required AND nullable: "this gate has no verdict" is a value the
		// client branches on, not a key it fails to find.
		"ApiGateEvidenceResponse": {
			"examined_artifact_id", "examined_artifact_type", "summary_artifact_id",
			"can_decide", "cannot_decide_reason", "wall_deadline",
		},
	}
	for name, want := range wantRequired {
		got := map[string]bool{}
		for _, r := range doc.Components.Schemas[name].Required {
			got[r] = true
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("%s: property %q is not required — openapi-typescript will render it optional", name, w)
			}
		}
	}

	// The other direction, and it is not symmetry for its own sake. A phase's
	// evidence is null on every non-gate phase and on every decided gate, and a
	// property that is a $ref carries NO null branch through this generator —
	// jsonschema-go only envelops nullability around a reference when the
	// reflector asks it to, which this one does not. Marking it required
	// therefore renders `evidence: ApiGateEvidenceResponse` in TypeScript: not
	// optional, not nullable, and wrong on most phases of most runs. Optional is
	// the honest half-truth, because it makes the client narrow before it reads.
	wantNotRequired := map[string][]string{"ApiPhaseResponse": {"evidence"}}
	for name, unwanted := range wantNotRequired {
		for _, r := range doc.Components.Schemas[name].Required {
			for _, u := range unwanted {
				if r == u {
					t.Errorf("%s: property %q is required, but the server sends null for it; "+
						"openapi-typescript renders a required $ref as non-nullable", name, u)
				}
			}
		}
	}
}
