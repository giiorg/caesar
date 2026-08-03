//go:build integration

package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

func TestAssetsAPI_CredentialsAdminOnlyAndSecretNeverEchoed(t *testing.T) {
	srv, _, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)
	const secret = "ghp_supersecrettokenvalue"

	// Member: 403 on every credentials operation.
	if resp := do(t, http.MethodPost, srv.URL+"/api/v1/credentials", "member-tok", assetsapi.CreateCredentialRequest{
		Name: "x", Kind: "forge_token", Secret: secret,
	}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create credential = %d, want 403", resp.StatusCode)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/credentials", "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list credentials = %d, want 403", resp.StatusCode)
	}

	// Admin roundtrip; the secret never appears in any response body.
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/credentials", "admin-tok", assetsapi.CreateCredentialRequest{
		Name: "github-bot", Kind: "forge_token", Secret: secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), secret) {
		t.Fatal("create response echoes the secret")
	}
	var created assetsapi.CredentialResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	resp = do(t, http.MethodPut, srv.URL+"/api/v1/credentials/"+created.ID+"/secret", "admin-tok",
		assetsapi.RotateCredentialSecretRequest{Secret: "rotated-secret-value"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate = %d", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	if strings.Contains(string(body), "rotated-secret-value") {
		t.Fatal("rotate response echoes the secret")
	}

	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}

func TestAssetsAPI_HarnessRegistryAdminOnly(t *testing.T) {
	srv, svc, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)

	cred, err := svc.CreateCredential(t.Context(), admin, "ant", "model_key", []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}

	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/harnesses", "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list harnesses = %d, want 403", resp.StatusCode)
	}

	resp := do(t, http.MethodPost, srv.URL+"/api/v1/harnesses", "admin-tok", assetsapi.RegisterHarnessRequest{
		Name: "claude-main", Kind: "claude-code",
		ImageRef: "ghcr.io/giiorg/caesar-harness-claude@sha256:abc", ProviderEndpoint: "https://api.anthropic.com",
		CredentialID: cred.ID.String(), Defaults: map[string]any{"model": "claude-sonnet-5"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
	h := decodeInto[assetsapi.HarnessResponse](t, resp)
	if h.Defaults["model"] != "claude-sonnet-5" {
		t.Fatalf("defaults = %+v", h.Defaults)
	}

	name := "claude-renamed"
	if resp := do(t, http.MethodPatch, srv.URL+"/api/v1/harnesses/"+h.ID, "admin-tok", assetsapi.UpdateHarnessRequest{Name: &name}); resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/harnesses/"+h.ID, "admin-tok", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}
