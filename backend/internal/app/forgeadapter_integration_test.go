//go:build integration

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// forgeToken is the secret these tests expect to travel to the forge. It is
// asserted on the wire, which is what makes "the credential was opened through
// the audited path" a claim about the actual request rather than about a
// counter.
const forgeToken = "ghs_forgeadaptertoken"

// forgeEnv is the smallest world a forgeAdapter test needs.
//
// The assets service is REAL and stands on a real pool, deliberately: the
// property under test is that the credential is opened through
// assets.UseCredential, whose audit entry is written inside the same
// transaction as the read. A double would let a version that called
// GetCredential — an unaudited read of the same secret — pass.
type forgeEnv struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	assets *assets.Service
	repo   runs.RepoRef
}

func newForgeEnv(t *testing.T) forgeEnv {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)

	clk := clock.System{}
	rec := audit.NewRecorder(clk)
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	identitySvc := identity.NewService(pool, clk, rec, identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	adminUser, err := identitySvc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	admin := authn.Principal{UserID: adminUser.ID, Email: adminUser.Email, Role: authn.RoleAdmin}

	assetsSvc := assets.NewService(pool, clk, rec, keys)
	project, err := assetsSvc.CreateProject(ctx, admin, "forge-adapter", "")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := assetsSvc.CreateCredential(ctx, admin, "github-bot",
		assets.CredentialForgeToken, []byte(forgeToken))
	if err != nil {
		t.Fatal(err)
	}
	repoRow, err := assetsSvc.CreateRepo(ctx, admin, project.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "acme/widgets", DefaultBranch: "main", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Through the production adapter, so the reference these tests hand the
	// forge is the one a real run would.
	repo, err := runsAssetSource{svc: assetsSvc}.Repo(ctx, repoRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	return forgeEnv{ctx: ctx, pool: pool, assets: assetsSvc, repo: repo}
}

// auditCount is this package's own: the identically-named helper in
// internal/assets is package-private there and invisible from here.
func (e forgeEnv) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx,
		"SELECT count(*) FROM audit_entries WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// forgeServing stands an httptest server up in front of a fresh adapter. Both
// forge kinds point at it so a test can swap repo.Forge without rewiring.
func forgeServing(t *testing.T, e forgeEnv, h http.HandlerFunc) forgeAdapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return newForgeAdapter(e.assets, forgeEndpoints{GitHub: srv.URL, GitLab: srv.URL}, srv.Client())
}

// status answers every request with one status code and a forge-shaped body.
func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"message":"the forge said so"}`)
	}
}

// spec §6: every credential use is audited. A forge call that read the secret
// with GetCredential would publish with an unaudited token — and the token
// assertion on the wire is what proves this opened the credential rather than
// happening to send something.
func TestForgeAdapter_OpensTheCredentialThroughTheAuditedPath(t *testing.T) {
	env := newForgeEnv(t)
	var sawAuth string
	fa := forgeServing(t, env, func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"sha":"deadbeef"}`)
	})

	sha, err := fa.ResolveHead(env.ctx, env.repo)
	if err != nil || sha != "deadbeef" {
		t.Fatalf("ResolveHead = %q, %v", sha, err)
	}
	if want := "Bearer " + forgeToken; sawAuth != want {
		t.Errorf("Authorization = %q, want %q — the opened credential is what must travel", sawAuth, want)
	}
	if n := env.auditCount(t, "credential.used"); n != 1 {
		t.Fatalf("credential.used audit entries = %d, want 1", n)
	}
}

func TestForgeAdapter_RefusesAnUnsupportedForgeBeforeOpeningACredential(t *testing.T) {
	env := newForgeEnv(t)
	repo := env.repo
	repo.Forge = "bitbucket"
	fa := forgeServing(t, env, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an unsupported forge must not reach a forge at all")
		w.WriteHeader(http.StatusTeapot)
	})

	if _, err := fa.ResolveHead(env.ctx, repo); err == nil {
		t.Fatal("an unsupported forge must fail")
	} else if got := xerrors.ClassOf(err); got != xerrors.ClassInvalid {
		t.Errorf("class = %v, want ClassInvalid: a forge kind caesar cannot speak is the "+
			"request's problem, and 500 would send an operator hunting a bug in caesar", got)
	}
	if n := env.auditCount(t, "credential.used"); n != 0 {
		t.Fatal("an unsupported forge must fail WITHOUT an audited secret use")
	}
}

// ResolveHead is the only Forge method on an HTTP path — both callers are task
// intake — so its failures must arrive at httpx with a class.
//
// This is the falsifier for typing it as runs.OutcomeError instead: xerrors
// answers ClassInternal for anything that is not an *xerrors.Error, so one
// shared error vocabulary would turn a repository the operator mistyped into a
// 500 with no detail.
func TestForgeAdapter_IntakeFailuresCarryTheClassTheHTTPLayerNeeds(t *testing.T) {
	tests := []struct {
		name  string
		code  int
		class xerrors.Class
	}{
		{"a repository the forge does not have", http.StatusNotFound, xerrors.ClassNotFound},
		{"a credential the forge rejects", http.StatusUnauthorized, xerrors.ClassInvalid},
		{"a forge that is not answering", http.StatusServiceUnavailable, xerrors.ClassRateLimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newForgeEnv(t)
			fa := forgeServing(t, env, status(tt.code))
			_, err := fa.ResolveHead(env.ctx, env.repo)
			if err == nil {
				t.Fatal("want an error")
			}
			if got := xerrors.ClassOf(err); got != tt.class {
				t.Fatalf("class = %v, want %v", got, tt.class)
			}
			var oe runs.OutcomeError
			if errors.As(err, &oe) {
				t.Error("intake must not speak the run vocabulary: an OutcomeError reaches " +
					"httpx unclassified and answers 500")
			}
		})
	}
}

// The run-path half of the contract: BranchHead, OpenPullRequest and
// FindPullRequests are called only from the publish state machine, which stands
// inside an intent that is 'pending' or 'pushed'. runs.ClassifyOutcome is
// default-deny, so an untyped error is a PERMANENT internal_error —
// unchargedWhileUnfinished does not downgrade permanents, evaluate terminalizes
// the run, and no sweep revisits a failed run. The branch is then an orphan.
//
// Only what the adapter can PROVE permanent is permanent. Everything else is
// transient, and only the three proven ones halt.
func TestForgeAdapter_RunPathFailuresAreTypedOutcomes(t *testing.T) {
	tests := []struct {
		name  string
		code  int
		class runs.OutcomeClass
		want  string
		// serve overrides the plain status handler where the ANSWER needs
		// headers to mean what it means — which is the whole story for 403.
		serve http.HandlerFunc
	}{
		{name: "a forge that is down", code: http.StatusServiceUnavailable, class: runs.OutcomeTransient, want: runs.CodeForgeUnavailable},
		{name: "a request that already exists", code: http.StatusUnprocessableEntity, class: runs.OutcomePermanent, want: runs.CodePublishConflict},
		{name: "a revoked credential", code: http.StatusUnauthorized, class: runs.OutcomePermanent, want: runs.CodeAuthExpired},
		{name: "a repository the forge will not show", code: http.StatusNotFound, class: runs.OutcomePermanent, want: runs.CodeRepoUnavailable},
		// A 403 carrying no rate-limit signal is a permission answer: an
		// under-scoped fine-grained PAT, an org enforcing SAML SSO, an archived
		// repository. It has to be PERMANENT. Falling to the default arm below
		// spends ~24 h of retries on a token that will never be allowed and then
		// settles with an expiry story, on a branch that is already pushed.
		{name: "a token the forge will not let act", code: http.StatusForbidden, class: runs.OutcomePermanent, want: runs.CodeAuthExpired},
		// And the reason that arm cannot simply read the status code: GitHub
		// signals both rate limits with 403 as well, and THAT one must stay a
		// transient with a reset. The two rows together pin the ordering.
		{
			name: "a 403 that is throttling, not refusal", code: http.StatusForbidden,
			class: runs.OutcomeTransient, want: runs.CodeForgeUnavailable,
			serve: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
			},
		},
		// The default arm, and the one that matters most: an answer nobody has
		// taught this adapter to read must NOT be permanent. Permanent here
		// terminalizes a run whose branch is already on the forge.
		{name: "an answer we cannot classify", code: http.StatusTeapot, class: runs.OutcomeTransient, want: runs.CodeForgeUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newForgeEnv(t)
			serve := tt.serve
			if serve == nil {
				serve = status(tt.code)
			}
			fa := forgeServing(t, env, serve)
			_, err := fa.OpenPullRequest(env.ctx, env.repo, runs.PullRequestSpec{Head: "caesar/x", Base: "main"})
			if err == nil {
				t.Fatal("want an error")
			}
			out := runs.ClassifyOutcome(err)
			if out.Class != tt.class || out.Code != tt.want {
				t.Fatalf("outcome = %+v, want %s/%s", out, tt.class, tt.want)
			}
		})
	}
}

// An ambiguous forge answer must come back UNCHARGED and RESUMABLE, not merely
// "an error". The downgrade only happens for transients, so a permanent default
// arm would spend nothing and strand everything.
func TestForgeAdapter_AnAmbiguousAnswerIsRetriedRatherThanTerminal(t *testing.T) {
	env := newForgeEnv(t)
	fa := forgeServing(t, env, status(http.StatusTeapot))
	_, err := fa.OpenPullRequest(env.ctx, env.repo, runs.PullRequestSpec{Head: "caesar/x", Base: "main"})
	out := runs.ClassifyOutcome(err)
	if !out.Retryable() {
		t.Fatalf("outcome = %+v; an answer we could not read must be asked again, not end the run", out)
	}
	// Chargeable is true here on purpose — unchargedWhileUnfinished is what
	// makes it uncharged, and it only reaches that code path for a TRANSIENT.
	if out.Class != runs.OutcomeTransient {
		t.Fatalf("outcome = %+v; only a transient is downgraded to an uncharged publish_resumable", out)
	}
}

// A forge that stated its own reset is taken at its word: retryDelay uses
// RetryAt verbatim, so waking earlier spends an attempt into a window we were
// told is shut — and on a secondary limit it extends the lockout.
func TestForgeAdapter_ARateLimitCarriesTheForgesOwnReset(t *testing.T) {
	env := newForgeEnv(t)
	fa := forgeServing(t, env, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", strconv.Itoa(90))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"slow down"}`)
	})
	_, err := fa.OpenPullRequest(env.ctx, env.repo, runs.PullRequestSpec{Head: "caesar/x", Base: "main"})
	out := runs.ClassifyOutcome(err)
	if out.Code != runs.CodeForgeUnavailable || out.RetryAt == nil {
		t.Fatalf("outcome = %+v, want a forge_unavailable carrying the stated reset", out)
	}
	if d := time.Until(*out.RetryAt); d < 80*time.Second || d > 100*time.Second {
		t.Errorf("RetryAt is %v out, want ~90s: the provider's word, untouched", d)
	}
}

// R4: the exits nothing else covers. typedForgeError-style wrapping only ever
// sees errors from the forge CALL; resolving the client and opening the
// credential happen before it, and returned raw they default-deny to a
// permanent internal_error on a live intent.
//
// Both legs are asserted on all three run-path methods, because the wrap is at
// each call site and a kill through one proves nothing about the others.
func TestForgeAdapter_EveryRunPathExitIsATypedUnchargedOutcome(t *testing.T) {
	legs := map[string]func(forgeEnv) runs.RepoRef{
		"an unsupported forge": func(e forgeEnv) runs.RepoRef {
			repo := e.repo
			repo.Forge = "bitbucket"
			return repo
		},
		"a credential that cannot be opened": func(e forgeEnv) runs.RepoRef {
			repo := e.repo
			repo.CredentialID = uuid.New()
			return repo
		},
	}
	for legName, mutate := range legs {
		t.Run(legName, func(t *testing.T) {
			env := newForgeEnv(t)
			repo := mutate(env)
			fa := forgeServing(t, env, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("nothing may reach the forge once open has failed")
				w.WriteHeader(http.StatusTeapot)
			})
			calls := map[string]func() error{
				"BranchHead": func() error {
					_, _, err := fa.BranchHead(env.ctx, repo, "caesar/x")
					return err
				},
				"OpenPullRequest": func() error {
					_, err := fa.OpenPullRequest(env.ctx, repo, runs.PullRequestSpec{Head: "caesar/x", Base: "main"})
					return err
				},
				"FindPullRequests": func() error {
					_, err := fa.FindPullRequests(env.ctx, repo, "caesar/x")
					return err
				},
			}
			for name, call := range calls {
				t.Run(name, func(t *testing.T) {
					err := call()
					if err == nil {
						t.Fatal("want an error")
					}
					out := runs.ClassifyOutcome(err)
					if out.Class != runs.OutcomeTransient || out.Code != runs.CodePublishNotStarted || out.Chargeable() {
						t.Fatalf("outcome = %+v; want a transient, uncharged publish_not_started — "+
							"nothing reached the forge, and a permanent here terminalizes a run "+
							"whose branch may already be pushed", out)
					}
				})
			}
		})
	}
}

// Both clone paths must follow the SAME configured root.
//
// cloneURL is one function with two production call sites — the runner's
// staging clone and the publication checkout — and they are asserted
// separately on purpose: a caller that reached for the public host directly
// would leave the other one right, and the run would stage from one server and
// push to another. Neither leg fails loudly; the run reads as "can see the
// repo, publishes nothing".
//
// Real assets service, real credential: prepare and CloneSpec both open one
// before they hand back a URL, so a double would not reach the line under test.
func TestConfiguredCloneRootsReachBothClonePaths(t *testing.T) {
	env := newForgeEnv(t)
	eps := forgeEndpoints{
		GitHub:      "https://git.example.internal/api/v3",
		GitHubClone: "https://git.example.internal",
	}
	const want = "https://git.example.internal/acme/widgets.git"

	t.Run("the runner's staging clone", func(t *testing.T) {
		spec, err := appGitSource{svc: env.assets, endpoints: eps}.CloneSpec(env.ctx, env.repo)
		if err != nil {
			t.Fatal(err)
		}
		if spec.URL != want {
			t.Errorf("staging clone URL = %q, want %q", spec.URL, want)
		}
	})

	t.Run("the publication checkout", func(t *testing.T) {
		in, _, err := gitPublisher{assets: env.assets, workDir: t.TempDir(), endpoints: eps}.
			prepare(env.ctx, runs.PushRequest{Repo: env.repo})
		if err != nil {
			t.Fatal(err)
		}
		if in.CloneURL != want {
			t.Errorf("publication clone URL = %q, want %q", in.CloneURL, want)
		}
	})
}

// Absence is an ANSWER for BranchHead, not a failure: recovery asks it first,
// and turning "the branch is not there" into an error would stop the machine
// from ever deciding to push.
func TestForgeAdapter_BranchHeadTreatsAbsenceAsAnAnswer(t *testing.T) {
	env := newForgeEnv(t)
	fa := forgeServing(t, env, status(http.StatusNotFound))
	sha, ok, err := fa.BranchHead(env.ctx, env.repo, "caesar/x")
	if err != nil {
		t.Fatalf("BranchHead = %v; a missing branch is an answer", err)
	}
	if ok || sha != "" {
		t.Fatalf("BranchHead = (%q, %v), want the branch reported absent", sha, ok)
	}
}

// The receipt fields all cross, Base above all: recovery finds a request by
// head branch alone, and without the base it would adopt one that merges
// somewhere nobody approved.
func TestForgeAdapter_FindPullRequestsCarriesTheWholeReceipt(t *testing.T) {
	env := newForgeEnv(t)
	fa := forgeServing(t, env, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, fmt.Sprintf(
			`[{"number":7,"html_url":"https://forge/pull/7","state":"closed",
			   "head":{"sha":"%s"},"base":{"ref":"release"}}]`, "abc123"))
	})
	got, err := fa.FindPullRequests(env.ctx, env.repo, "caesar/x")
	if err != nil {
		t.Fatal(err)
	}
	want := []runs.PullRequestRef{{
		Number: 7, URL: "https://forge/pull/7", HeadSHA: "abc123", Base: "release", State: "closed",
	}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("FindPullRequests = %+v, want %+v", got, want)
	}
}
