//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// fakePorts implements the authn ports and GrantSource without identity:
// assets tests may never import the identity context (arch law). Real
// wiring is exercised by the app e2e (Task 12).
type fakePorts struct {
	sessions map[string]authn.AuthSession
	grants   map[uuid.UUID][]authn.Grant
}

func (f *fakePorts) AuthenticateSession(_ context.Context, token string) (authn.AuthSession, error) {
	s, ok := f.sessions[token]
	if !ok {
		return authn.AuthSession{}, xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")
	}
	return s, nil
}

func (f *fakePorts) Authorize(_ context.Context, p authn.Principal, projectID uuid.UUID, min authn.GrantLevel) (bool, error) {
	if p.Role == authn.RoleAdmin {
		return true, nil
	}
	for _, g := range f.grants[p.UserID] {
		if g.ProjectID == projectID && g.Level.AtLeast(min) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakePorts) GrantsForUser(_ context.Context, userID uuid.UUID) ([]authn.Grant, error) {
	return f.grants[userID], nil
}

func seedUser(t *testing.T, pool *pgxpool.Pool, email string, role authn.Role) authn.Principal {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id",
		email, string(role)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return authn.Principal{UserID: id, Email: email, Role: role}
}

func newTestServer(t *testing.T) (*httptest.Server, *assets.Service, *pgxpool.Pool, *fakePorts) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	svc := assets.NewService(pool, fake, audit.NewRecorder(fake), keys)
	ports := &fakePorts{sessions: map[string]authn.AuthSession{}, grants: map[uuid.UUID][]authn.Grant{}}

	mux := http.NewServeMux()
	httpx.Mount(mux, assetsapi.Operations(assetsapi.New(svc, ports, ports, ports)))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test")))
	t.Cleanup(srv.Close)
	return srv, svc, pool, ports
}

func login(ports *fakePorts, token string, p authn.Principal) {
	ports.sessions[token] = authn.AuthSession{Principal: p, SessionID: uuid.New()}
}

func do(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Cookie", authn.SessionCookie+"="+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeInto[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAssetsAPI_ProjectVisibilityFollowsGrants(t *testing.T) {
	srv, _, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)

	// Admin creates two projects over the API; member creation is 403.
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects", "admin-tok", assetsapi.CreateProjectRequest{Name: "alpha"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	alpha := decodeInto[assetsapi.ProjectResponse](t, resp)
	if resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects", "admin-tok", assetsapi.CreateProjectRequest{Name: "beta"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create beta = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects", "member-tok", assetsapi.CreateProjectRequest{Name: "gamma"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create = %d, want 403", resp.StatusCode)
	}

	// No grant: empty list, 403 on get.
	if got := decodeInto[assetsapi.ProjectListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects", "member-tok", nil)); len(got.Projects) != 0 {
		t.Fatalf("ungranted member sees %d projects", len(got.Projects))
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+alpha.ID, "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ungranted get = %d, want 403", resp.StatusCode)
	}

	// View grant on alpha: visible, readable, still not writable.
	alphaID := uuid.MustParse(alpha.ID)
	ports.grants[member.UserID] = []authn.Grant{{ProjectID: alphaID, Level: authn.GrantView}}
	if got := decodeInto[assetsapi.ProjectListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects", "member-tok", nil)); len(got.Projects) != 1 || got.Projects[0].Name != "alpha" {
		t.Fatalf("granted member list = %+v", got.Projects)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+alpha.ID, "member-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("granted get = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPatch, srv.URL+"/api/v1/projects/"+alpha.ID, "member-tok", assetsapi.UpdateProjectRequest{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("view-level patch = %d, want 403", resp.StatusCode)
	}

	// Admin lists everything; no cookie is 401.
	if got := decodeInto[assetsapi.ProjectListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects", "admin-tok", nil)); len(got.Projects) != 2 {
		t.Fatalf("admin list = %d", len(got.Projects))
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", resp.StatusCode)
	}
}

func TestAssetsAPI_RepoEndpointsEnforceManage(t *testing.T) {
	ctx := context.Background()
	srv, svc, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)

	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}

	// manage grant → member can register a repo.
	ports.grants[member.UserID] = []authn.Grant{{ProjectID: p.ID, Level: authn.GrantManage}}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects/"+p.ID.String()+"/repos", "member-tok", assetsapi.CreateRepoRequest{
		Forge: "github", Slug: "giiorg/caesar", CredentialID: cred.ID.String(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create repo = %d", resp.StatusCode)
	}
	repo := decodeInto[assetsapi.RepoResponse](t, resp)

	// Downgrade to run: reads still work, writes are 403.
	ports.grants[member.UserID] = []authn.Grant{{ProjectID: p.ID, Level: authn.GrantRun}}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/repos/"+repo.ID, "member-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("get repo = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+p.ID.String()+"/repos", "member-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("list repos = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/repos/"+repo.ID, "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("run-level delete = %d, want 403", resp.StatusCode)
	}

	// Bad path uuid → 404 problem, unknown body field → 400.
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/repos/not-a-uuid", "admin-tok", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad uuid = %d", resp.StatusCode)
	}
	raw, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/projects", bytes.NewReader([]byte(`{"name":"x","surprise":1}`)))
	raw.Header.Set("Cookie", authn.SessionCookie+"=admin-tok")
	rawResp, err := http.DefaultClient.Do(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", rawResp.StatusCode)
	}
}

// A repo held by a task answers 409 with a body that says which repo problem
// it is. Before this classification the same request was a 500 "internal
// error", which a UI can only render as an outage.
func TestAssetsAPI_DeleteRepoInUseIsAConflict(t *testing.T) {
	ctx := context.Background()
	srv, svc, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	login(ports, "admin-tok", admin)

	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/caesar", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Raw: assets never imports the execution spine, so there is no intake here.
	var versionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH w AS (INSERT INTO workflows (name) VALUES ('wf') RETURNING id)
		INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		SELECT w.id, 1, 'h', '{}' FROM w RETURNING id`).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		VALUES ($1, $2, $3, 'a task', $4, now())`,
		p.ID, repo.ID, versionID, admin.UserID); err != nil {
		t.Fatal(err)
	}

	resp := do(t, http.MethodDelete, srv.URL+"/api/v1/repos/"+repo.ID.String(), "admin-tok", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete in-use repo = %d, want 409", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json", ct)
	}
	pd := decodeInto[httpx.ProblemDetails](t, resp)
	if pd.Title != "Conflict" || pd.Status != http.StatusConflict {
		t.Fatalf("problem = %+v, want a conflict envelope", pd)
	}
	if !strings.Contains(pd.Detail, "tasks") {
		t.Fatalf("detail = %q, want it to name tasks", pd.Detail)
	}
	if pd.CorrelationID == "" {
		t.Fatal("a 409 must still carry the correlation id")
	}
	// That the repo survives is asserted at the service tier, and only as
	// documentation: the rollback guarantees it whatever the classification
	// does. Repeating it here would add a second assertion that cannot fail.
}

func TestAssetsAPI_CredentialFreshnessFields(t *testing.T) {
	ctx := context.Background()
	srv, svc, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	login(ports, "admin-tok", admin)

	// A full-size auth.json must survive the HTTP layer: the request body
	// limit has to leave room for the 64 KiB the kind allows.
	pad := strings.Repeat("x", 64<<10-len(`{"tokens":{"refresh_token":"rt-1"},"pad":""}`))
	body := `{"tokens":{"refresh_token":"rt-1"},"pad":"` + pad + `"}`
	if len(body) != 64<<10 {
		t.Fatalf("fixture is %d bytes, want %d", len(body), 64<<10)
	}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/credentials", "admin-tok",
		assetsapi.CreateCredentialRequest{Name: "codex-sub", Kind: "agent_auth_file", Secret: body})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201 for a 64 KiB auth.json", resp.StatusCode)
	}
	created := decodeInto[assetsapi.CredentialResponse](t, resp)
	if created.Kind != "agent_auth_file" || created.SecretVersion != 1 {
		t.Fatalf("created = %+v", created)
	}
	if !created.ProvidedAt.Equal(created.UpdatedAt) {
		t.Fatalf("provided_at = %s, want updated_at %s", created.ProvidedAt, created.UpdatedAt)
	}
	if created.LastUsedAt != nil || created.LastAuthExpiredAt != nil {
		t.Fatalf("freshness stamps = %v/%v, want unset on create", created.LastUsedAt, created.LastAuthExpiredAt)
	}
	// The unset stamps stay off the wire entirely.
	raw := decodeInto[map[string]any](t, do(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil))
	if _, ok := raw["last_used_at"]; ok {
		t.Fatal("last_used_at must be omitted while NULL")
	}

	// The secret round-trips byte for byte, and using it stamps the view.
	id := uuid.MustParse(created.ID)
	plain, version, err := svc.UseCredentialVersioned(ctx, id, "materialize-auth")
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != body || version != 1 {
		t.Fatalf("use = %d bytes at version %d, want %d bytes at version 1", len(plain), version, len(body))
	}
	got := decodeInto[assetsapi.CredentialResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil))
	if got.LastUsedAt == nil {
		t.Fatal("last_used_at must be set after a use")
	}

	if resp := do(t, http.MethodPut, srv.URL+"/api/v1/credentials/"+created.ID+"/secret", "admin-tok",
		assetsapi.RotateCredentialSecretRequest{Secret: `{"tokens":{"refresh_token":"rt-2"}}`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate = %d", resp.StatusCode)
	}
	if got := decodeInto[assetsapi.CredentialResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil)); got.SecretVersion != 2 {
		t.Fatalf("secret_version = %d after rotate, want 2", got.SecretVersion)
	}
}
