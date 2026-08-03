package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

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
