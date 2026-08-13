package contracts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/contracts"
)

func TestBuild_ProducesOpenAPI31WithAllOperations(t *testing.T) {
	raw, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi = %q, want 3.1.x", doc.OpenAPI)
	}
	if doc.Info.Title != "caesar API" {
		t.Fatalf("title = %q", doc.Info.Title)
	}
	wantOps := map[string]map[string]string{
		"/api/v1/auth/login":    {"post": "login"},
		"/api/v1/auth/logout":   {"post": "logout"},
		"/api/v1/auth/me":       {"get": "me"},
		"/api/v1/auth/password": {"put": "changePassword"},
		"/api/v1/users":         {"post": "createUser", "get": "listUsers"},
		"/api/v1/users/{id}":    {"get": "getUser", "patch": "updateUser"},
	}
	for path, methods := range wantOps {
		got, ok := doc.Paths[path]
		if !ok {
			t.Errorf("missing path %s", path)
			continue
		}
		for method, opID := range methods {
			if got[method].OperationID != opID {
				t.Errorf("%s %s operationId = %q, want %q", method, path, got[method].OperationID, opID)
			}
		}
	}
}

func TestBuild_IsDeterministic(t *testing.T) {
	a, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	b, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("Build must be deterministic — the regen-diff gate depends on it")
	}
}

// Operation.Errors is the only way an endpoint can declare a non-2xx answer
// beyond the 400 every operation documents. Without a test the mechanism is
// guarded only by the committed document, which says nothing about WHY the
// status is there — and a client that cannot see 409 in the contract reads a
// legitimate conflict as a bug.
func TestBuild_DocumentsTheErrorStatusesAnOperationDeclares(t *testing.T) {
	raw, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Responses   map[string]struct {
				Content map[string]any `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	decide := doc.Paths["/api/v1/runs/{id}/gates/{node}/decision"]["post"]
	if decide.OperationID != "decideGate" {
		t.Fatalf("operationId = %q, want decideGate", decide.OperationID)
	}
	conflict, ok := decide.Responses["409"]
	if !ok {
		t.Fatalf("decideGate responses = %v, want a documented 409", sortedKeys(decide.Responses))
	}
	if _, ok := conflict.Content["application/problem+json"]; !ok {
		t.Errorf("409 content = %v, want the problem+json envelope", conflict.Content)
	}

	// And only where it is declared: an operation that lists no extra statuses
	// keeps the two it always had, so the loop cannot be documenting 409
	// everywhere.
	listed := doc.Paths["/api/v1/approvals"]["get"]
	if got := sortedKeys(listed.Responses); len(got) != 2 || got[0] != "200" || got[1] != "400" {
		t.Errorf("listApprovals responses = %v, want only 200 and 400", got)
	}
}

// contentTypes is the media-type set an operation's success response carries,
// and the header parameters it declares — the two things Plan 10 added to this
// document that nothing else in this file would notice.
type opDoc struct {
	OperationID string `json:"operationId"`
	Parameters  []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema struct {
				Ref string `json:"$ref"`
			} `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

func buildPaths(t *testing.T) map[string]map[string]opDoc {
	t.Helper()
	raw, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]opDoc `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Paths
}

// The negotiated endpoint answers two media types under ONE status, and the
// document has to say so. If the SSE response replaced the poll's
// application/json instead of joining it, every generated client would lose
// the poll — which is the shipped contract, not the new thing.
func TestBuild_DocumentsBothMediaTypesOnTheNegotiatedEventsEndpoint(t *testing.T) {
	t.Parallel()
	op := buildPaths(t)["/api/v1/runs/{id}/events"]["get"]
	if op.OperationID != "listRunEvents" {
		t.Fatalf("operationId = %q, want listRunEvents", op.OperationID)
	}
	ok200, ok := op.Responses["200"]
	if !ok {
		t.Fatalf("responses = %v, want a 200", sortedKeys(op.Responses))
	}
	if got := sortedKeys(ok200.Content); len(got) != 2 || got[0] != "application/json" || got[1] != "text/event-stream" {
		t.Fatalf("200 media types = %v, want application/json and text/event-stream", got)
	}
	// Two media types describing the same schema would mean one of them is
	// documenting the wrong body.
	if a, b := ok200.Content["application/json"].Schema.Ref, ok200.Content["text/event-stream"].Schema.Ref; a == b {
		t.Errorf("both media types point at %q; the poll's envelope and one stream frame are different shapes", a)
	}

	// The resume mechanism is invisible to a client that cannot see the header.
	if !hasParam(op, "header", "Last-Event-ID") {
		t.Errorf("parameters = %+v, want a Last-Event-ID header", op.Parameters)
	}
}

func TestBuild_DocumentsTheAttentionStream(t *testing.T) {
	t.Parallel()
	op := buildPaths(t)["/api/v1/stream"]["get"]
	if op.OperationID != "streamAttention" {
		t.Fatalf("operationId = %q, want streamAttention", op.OperationID)
	}
	if got := sortedKeys(op.Responses["200"].Content); len(got) != 1 || got[0] != "text/event-stream" {
		t.Errorf("200 media types = %v, want only text/event-stream", got)
	}
	if !hasParam(op, "header", "Last-Event-ID") {
		t.Errorf("parameters = %+v, want a Last-Event-ID header", op.Parameters)
	}
}

func hasParam(op opDoc, in, name string) bool {
	for _, p := range op.Parameters {
		if p.In == in && p.Name == name {
			return true
		}
	}
	return false
}

// The regen-diff gate in one test: what Build produces is what is committed.
func TestBuild_MatchesTheCommittedDocument(t *testing.T) {
	t.Parallel()
	raw, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(committed), string(raw)); diff != "" {
		t.Errorf("generated document drifted from the committed one (-committed +generated):\n%s", diff)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
