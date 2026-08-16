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
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
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
		// The run list scopes rather than refuses, exactly as /approvals does:
		// an ungranted member gets 200 and an empty page, never a 403.
		"/api/v1/runs": {"get": {"200", "400", "401"}},
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
		// The run list and the run page. next_cursor, started_at, finished_at
		// and a phase summary's status are required AND nullable — every one of
		// them is a value the client branches on, not a key it fails to find,
		// and none of them is a $ref, so the generator really does render
		// ["type","null"] beside the required entry.
		"ApiRunListPageResponse":  {"runs", "next_cursor"},
		"ApiPhaseSummaryResponse": {"node_id", "node_type", "status", "attempt", "generation"},
		"ApiRunSummaryResponse": {
			"id", "task_id", "task_title", "project_id", "project_name", "repo_slug",
			"workflow_name", "status", "attempt", "generation", "requester_id",
			"created_at", "started_at", "finished_at", "phase_summary",
		},
		"ApiRunResponse": {
			"id", "task_id", "task_title", "repo_slug", "workflow_name", "status",
			"definition_hash", "base_sha", "policy_snapshot", "generation",
			"execution_used_ms", "wall_deadline", "created_at", "started_at",
			"finished_at", "phases", "artifacts", "publications",
		},
		"ApiPhaseResponse": {
			"node_id", "attempt", "max_attempts", "status", "generation",
			"created_at", "finished_at",
		},
		"ApiArtifactResponse": {
			"id", "node_id", "attempt", "generation", "type", "parent_ids", "created_at",
		},
		"ApiRunListResponse": {"runs"},
		// The artifact payload contract (document schema 2). These five reach
		// the document through contracts' extraSchemas list rather than through
		// an operation, so this is also the pin that fails if that mechanism
		// stops working: every wanted property reads as absent when the schema
		// is missing entirely.
		"ApiPlanPayload":          {"summary", "steps"},
		"ApiPlanStep":             {"title"},
		"ApiReviewVerdictPayload": {"verdict", "findings"},
		"ApiReviewFinding":        {"severity", "message"},
		"ApiTestOutcome":          {"status"},
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
	//
	// This loop asserts an ABSENCE, so it has to prove it is looking at
	// something: a renamed schema or a renamed property would satisfy "not in
	// required" vacuously, and the pin would go quiet exactly when the thing it
	// pins moved. The loop above fails loudly on a missing schema for free —
	// every wanted property reads as absent — and this one has to say so.
	//
	// The set has grown with Plan 12a's markup sweep and every entry is the
	// same shape: a $ref-typed property the server genuinely sends as null.
	// A phase's outcome is null until the attempt ends and its decision until
	// somebody decides it; a run's outcome is null on every run still going.
	// Marking any of them required renders `outcome: ApiOutcomeResponse` — not
	// optional, not nullable, and wrong on most rows of most pages.
	wantNotRequired := map[string][]string{
		"ApiPhaseResponse": {"evidence", "decision", "outcome"},
		"ApiRunResponse":   {"outcome"},
		// A review verdict's `tests` is the same trade one layer down, and the
		// only one in 12a where the validator REQUIRES the key: the payload
		// contract says tests must be present and may be null, and this
		// generator cannot say "this object or null" for a named schema.
		// payload_schema_test.go holds the two descriptions together across the
		// exception.
		"ApiReviewVerdictPayload": {"tests"},
	}
	for name, unwanted := range wantNotRequired {
		schema, ok := doc.Components.Schemas[name]
		if !ok {
			t.Errorf("%s: no such schema — this pin asserts an absence and cannot do it against nothing", name)
			continue
		}
		required := map[string]bool{}
		for _, r := range schema.Required {
			required[r] = true
		}
		for _, u := range unwanted {
			if _, ok := schema.Properties[u]; !ok {
				t.Errorf("%s: no property %q — renamed or dropped, and this pin no longer pins it", name, u)
				continue
			}
			if required[u] {
				t.Errorf("%s: property %q is required, but the server sends null for it; "+
					"openapi-typescript renders a required $ref as non-nullable", name, u)
			}
		}
	}
}
