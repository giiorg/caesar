//go:build integration

package api

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

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

const testPassword = "a long enough password"

// newTestServer mirrors app wiring: correlation → access log → origin →
// mounted operations. The app-level e2e (Task 12) covers the real Routes.
func newTestServer(t *testing.T) (*httptest.Server, *identity.Service) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	a := New(svc, 90*24*time.Hour)
	mux := http.NewServeMux()
	httpx.Mount(mux, Operations(a))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test")))
	t.Cleanup(srv.Close)
	return srv, svc
}

func do(t *testing.T, method, url, cookie string, body any) *http.Response {
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
	if cookie != "" {
		req.Header.Set("Cookie", SessionCookie+"="+cookie)
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

// sessionCookie extracts the session token from Set-Cookie and asserts
// the pinned attributes.
func sessionCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			if !c.HttpOnly || !c.Secure || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie attributes wrong: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("no session cookie set")
	return ""
}

func bootstrapAndLogin(t *testing.T, srv *httptest.Server, svc *identity.Service) string {
	t.Helper()
	if _, err := svc.Bootstrap(context.Background(), "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", LoginRequest{Email: "root@example.com", Password: testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	return sessionCookie(t, resp)
}

func TestAPI_LoginMeLogout(t *testing.T) {
	srv, svc := newTestServer(t)
	token := bootstrapAndLogin(t, srv, svc)

	resp := do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me status = %d", resp.StatusCode)
	}
	me := decodeInto[MeResponse](t, resp)
	if me.User.Email != "root@example.com" || me.User.Role != "admin" {
		t.Fatalf("me = %+v", me)
	}

	resp = do(t, http.MethodPost, srv.URL+"/api/v1/auth/logout", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d", resp.StatusCode)
	}
}

func TestAPI_ProblemEnvelopeShape(t *testing.T) {
	srv, svc := newTestServer(t)
	if _, err := svc.Bootstrap(context.Background(), "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", LoginRequest{Email: "root@example.com", Password: "wrong password!!"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	pd := decodeInto[httpx.ProblemDetails](t, resp)
	if pd.Type != "/errors/unauthenticated" || pd.CorrelationID == "" {
		t.Fatalf("problem = %+v", pd)
	}
}

func TestAPI_UserAdministration(t *testing.T) {
	srv, svc := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)

	// Create a member.
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/users", admin, CreateUserRequest{
		Email: "m@example.com", Password: testPassword, Role: "member",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	member := decodeInto[UserResponse](t, resp)

	// Member logs in; admin endpoints are forbidden to them.
	resp = do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", LoginRequest{Email: "m@example.com", Password: testPassword})
	memberTok := sessionCookie(t, resp)
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/users", memberTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list users = %d", resp.StatusCode)
	}

	// Unknown JSON fields are rejected (strict decode).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users", strings.NewReader(`{"email":"x@example.com","password":"long enough password","role":"member","surprise":true}`))
	req.Header.Set("Cookie", SessionCookie+"="+admin)
	rawResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", rawResp.StatusCode)
	}

	// Admin lists, gets, patches.
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/users", admin, nil)
	if got := decodeInto[UserListResponse](t, resp); len(got.Users) != 2 {
		t.Fatalf("users = %d, want 2", len(got.Users))
	}
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/users/"+member.ID, admin, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	role := "admin"
	resp = do(t, http.MethodPatch, srv.URL+"/api/v1/users/"+member.ID, admin, UpdateUserRequest{Role: &role})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	// Privilege change revoked the member's session.
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", memberTok, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("member session after promote = %d", resp.StatusCode)
	}
}

func TestAPI_ChangePasswordRotatesCookie(t *testing.T) {
	srv, svc := newTestServer(t)
	token := bootstrapAndLogin(t, srv, svc)

	resp := do(t, http.MethodPut, srv.URL+"/api/v1/auth/password", token, ChangePasswordRequest{
		CurrentPassword: testPassword, NewPassword: "an even better password",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change password status = %d", resp.StatusCode)
	}
	fresh := sessionCookie(t, resp)
	if fresh == token {
		t.Fatal("cookie must rotate")
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", token, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("old session must be dead")
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", fresh, nil); resp.StatusCode != http.StatusOK {
		t.Fatal("fresh session must work")
	}
}
