package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/forge"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// runsDefinitionSource adapts workflows.Service to runs.DefinitionSource:
// parse the stored document with the definitions context's own parser,
// then map onto the runs mirror mechanically. Only app may see both
// vocabularies (composition root).
type runsDefinitionSource struct{ svc *workflows.Service }

func (s runsDefinitionSource) Definition(ctx context.Context, workflowID uuid.UUID, version int32) (runs.PinnedDefinition, error) {
	v, err := s.svc.GetVersion(ctx, workflowID, version)
	if err != nil {
		return runs.PinnedDefinition{}, err
	}
	return pinnedFromVersion(v)
}

func (s runsDefinitionSource) DefinitionByVersionID(ctx context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	v, err := s.svc.GetVersionByID(ctx, versionID)
	if err != nil {
		return runs.PinnedDefinition{}, err
	}
	return pinnedFromVersion(v)
}

func pinnedFromVersion(v workflows.Version) (runs.PinnedDefinition, error) {
	doc, err := workflows.ParseDocument(v.Document)
	if err != nil {
		return runs.PinnedDefinition{}, fmt.Errorf("app: stored definition %s unparseable: %w", v.ID, err)
	}
	mirror := runs.DefinitionDoc{}
	for _, n := range doc.Nodes {
		mn := runs.DefNode{
			ID: n.ID, Kind: string(n.Type), Harness: n.Harness, Instructions: n.Instructions,
		}
		if n.OnRequestChanges != nil {
			// Only app sees both vocabularies (composition root): the mapping
			// is mechanical, and the runs mirror owns its own type so the
			// contexts stay peers.
			mn.Revision = &runs.DefRevision{
				Target:    n.OnRequestChanges.Target,
				MaxRounds: n.OnRequestChanges.MaxRounds,
			}
		}
		mirror.Nodes = append(mirror.Nodes, mn)
	}
	for _, e := range doc.Edges {
		mirror.Edges = append(mirror.Edges, runs.DefEdge{From: e.From, To: e.To, Artifact: string(e.Artifact)})
	}
	return runs.PinnedDefinition{
		VersionID: v.ID, WorkflowID: v.WorkflowID, Version: v.Version,
		ContentHash: v.ContentHash, Doc: mirror,
	}, nil
}

// runsAssetSource adapts assets.Service to RepoSource + HarnessSource.
type runsAssetSource struct{ svc *assets.Service }

func (s runsAssetSource) Repo(ctx context.Context, id uuid.UUID) (runs.RepoRef, error) {
	r, err := s.svc.GetRepo(ctx, id)
	if err != nil {
		return runs.RepoRef{}, err
	}
	return runs.RepoRef{
		ID: r.ID, ProjectID: r.ProjectID, CredentialID: r.CredentialID,
		Forge: string(r.Forge), Slug: r.Slug, DefaultBranch: r.DefaultBranch,
	}, nil
}

// HarnessByName resolves the harness and, with it, the kind of credential it
// authenticates with: the spine pins both into the attempt's binding, so the
// runner never re-reads the mutable harness row to learn either.
func (s runsAssetSource) HarnessByName(ctx context.Context, name string) (runs.HarnessRef, error) {
	h, err := s.svc.GetHarnessByName(ctx, name)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	c, err := s.svc.GetCredential(ctx, h.CredentialID)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	return runs.HarnessRef{
		ID: h.ID, Kind: string(h.Kind),
		CredentialID: h.CredentialID, CredentialKind: string(c.Kind),
	}, nil
}

// StaleCredentialLeases adapts the assets aggregate onto the orchestrator's
// janitor port. Only the two fields the janitor acts on cross the boundary:
// everything else about a lease is the aggregate's business.
func (s runsAssetSource) StaleCredentialLeases(ctx context.Context, olderThan time.Duration) ([]runs.StaleLease, error) {
	leases, err := s.svc.ListStaleCredentialLeases(ctx, olderThan)
	if err != nil {
		return nil, err
	}
	out := make([]runs.StaleLease, 0, len(leases))
	for _, l := range leases {
		out = append(out, runs.StaleLease{CredentialID: l.CredentialID, AttemptRef: l.AttemptRef})
	}
	return out, nil
}

// runsLabelSource adapts assets + workflows to runs.RunLabelSource. The loops
// are over DISTINCT ids the caller already deduplicated, so this is O(distinct)
// round trips per page and not O(rows) — and the port's shape leaves room for a
// real batch query later without touching the runs context.
//
// A deleted asset is a MISSING LABEL and never a failed page: the run rows
// outlive the repository and the workflow they name, and a list that 500s
// because one of them was removed is a list nobody can use to find out what
// happened.
type runsLabelSource struct {
	assets    *assets.Service
	workflows *workflows.Service
}

// The port is consumed only through runs.Params, so this is what keeps a
// signature drift from surviving until production wiring.
var _ runs.RunLabelSource = runsLabelSource{}

func (s runsLabelSource) ProjectNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	list, err := s.assets.ListProjectsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(list))
	for _, p := range list {
		out[p.ID] = p.Name
	}
	return out, nil
}

func (s runsLabelSource) RepoSlugs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		r, err := s.assets.GetRepo(ctx, id)
		if err != nil {
			// A deleted repo is a missing label, not a failed page.
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				continue
			}
			return nil, err
		}
		out[id] = r.Slug
	}
	return out, nil
}

func (s runsLabelSource) WorkflowNames(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(versionIDs))
	byWorkflow := map[uuid.UUID]string{}
	for _, vid := range versionIDs {
		v, err := s.workflows.GetVersionByID(ctx, vid)
		if err != nil {
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				continue
			}
			return nil, err
		}
		name, ok := byWorkflow[v.WorkflowID]
		if !ok {
			wf, err := s.workflows.GetWorkflow(ctx, v.WorkflowID)
			if err != nil {
				if xerrors.ClassOf(err) == xerrors.ClassNotFound {
					continue
				}
				return nil, err
			}
			name = wf.Name
			byWorkflow[v.WorkflowID] = name
		}
		out[vid] = name
	}
	return out, nil
}

// forgeAdapter implements runs.Forge over the platform clients. It is the
// composition root's job because it needs BOTH the runs vocabulary and the
// assets credential path, and no context may see both.
//
// The credential is opened per call through the AUDITED path
// (assets.UseCredential), never cached on the adapter: a cached token would
// outlive the audit entry that justifies it (spec §6, "every use audited").
//
// EVERY exit of EVERY method is typed, and the vocabularies are deliberately
// SPLIT — see intakeError and publishError. The port's own doc comment states
// the run-path half of the contract, because runs.ClassifyOutcome is
// default-deny: an untyped error out of OpenPullRequest becomes a PERMANENT
// internal_error that unchargedWhileUnfinished will not downgrade, and the run
// terminalizes with its branch already live on the forge and no sweep that
// revisits it.
type forgeAdapter struct {
	assets *assets.Service
	github *forge.GitHub
	gitlab *forge.GitLab
}

// The port is not consumed inside app, so this is what keeps a signature drift
// from surviving until production wiring.
var _ runs.Forge = forgeAdapter{}

// newForgeAdapter is the one construction site, shared by serve and the tests
// so a wiring change cannot pass in one and fail in the other.
func newForgeAdapter(a *assets.Service, eps forgeEndpoints, hc *http.Client) forgeAdapter {
	return forgeAdapter{
		assets: a,
		github: forge.NewGitHub(eps.GitHub, hc),
		gitlab: forge.NewGitLab(eps.GitLab, hc),
	}
}

// forgeRepo is the whole translation from the spine's repo reference to the
// platform's. DefaultBranch travels because Head resolves it by name: dropping
// it silently resolves the wrong ref, and a run would pin a base SHA off a
// branch nobody chose.
func forgeRepo(repo runs.RepoRef) forge.Repo {
	return forge.Repo{Slug: repo.Slug, DefaultBranch: repo.DefaultBranch}
}

func (f forgeAdapter) client(repo runs.RepoRef) (forge.Client, error) {
	switch repo.Forge {
	case "github":
		return f.github, nil
	case "gitlab":
		return f.gitlab, nil
	}
	return nil, xerrors.E(xerrors.ClassInvalid, fmt.Sprintf("unsupported forge %q", repo.Forge))
}

// open resolves the client and the credential in that order: an unsupported
// forge must fail WITHOUT an audited secret use.
//
// Its two failures are returned RAW and every caller types them, because the
// right type depends on which caller: on the HTTP path a mistyped forge is the
// requester's 400, and on the run path it is a transient non-start.
func (f forgeAdapter) open(ctx context.Context, repo runs.RepoRef, purpose string) (forge.Client, forge.Auth, error) {
	c, err := f.client(repo)
	if err != nil {
		return nil, forge.Auth{}, err
	}
	token, err := f.assets.UseCredential(ctx, repo.CredentialID, purpose+":"+repo.Slug)
	if err != nil {
		return nil, forge.Auth{}, err
	}
	return c, forge.Auth{Token: string(token)}, nil
}

// ResolveHead is the only Forge method on an HTTP path — both callers are task
// intake — so it speaks xerrors, not runs.Outcome. See intakeError.
func (f forgeAdapter) ResolveHead(ctx context.Context, repo runs.RepoRef) (string, error) {
	c, auth, err := f.open(ctx, repo, "resolve_head")
	if err != nil {
		return "", intakeError("resolve head", repo, err)
	}
	sha, err := c.Head(ctx, forgeRepo(repo), auth)
	if err != nil {
		return "", intakeError("resolve head", repo, err)
	}
	return sha, nil
}

// BranchHead is recovery's first question, so absence is an ANSWER and not an
// error: the platform clients already fold a 404 into ok=false, and turning it
// back into a failure would stop recoverBranch from ever deciding to push.
func (f forgeAdapter) BranchHead(ctx context.Context, repo runs.RepoRef, branch string) (string, bool, error) {
	c, auth, err := f.open(ctx, repo, "branch_head")
	if err != nil {
		return "", false, notStarted(fmt.Errorf("probe branch %s: %w", branch, err))
	}
	sha, ok, err := c.BranchHead(ctx, forgeRepo(repo), auth, branch)
	if err != nil {
		return "", false, publishError(fmt.Sprintf("probe branch %s", branch), repo, err)
	}
	return sha, ok, nil
}

func (f forgeAdapter) OpenPullRequest(ctx context.Context, repo runs.RepoRef, spec runs.PullRequestSpec) (runs.PullRequestRef, error) {
	c, auth, err := f.open(ctx, repo, "open_pull_request")
	if err != nil {
		return runs.PullRequestRef{}, notStarted(fmt.Errorf("open pull request: %w", err))
	}
	pr, err := c.CreatePullRequest(ctx, forgeRepo(repo), auth, forge.PullRequest{
		Head: spec.Head, Base: spec.Base, Title: spec.Title, Body: spec.Body,
	})
	if err != nil {
		return runs.PullRequestRef{}, publishError("open pull request", repo, err)
	}
	return pullRequestRef(pr), nil
}

func (f forgeAdapter) FindPullRequests(ctx context.Context, repo runs.RepoRef, headBranch string) ([]runs.PullRequestRef, error) {
	c, auth, err := f.open(ctx, repo, "find_pull_requests")
	if err != nil {
		return nil, notStarted(fmt.Errorf("probe pull requests on %s: %w", headBranch, err))
	}
	found, err := c.FindPullRequests(ctx, forgeRepo(repo), auth, headBranch)
	if err != nil {
		return nil, publishError(fmt.Sprintf("probe pull requests on %s", headBranch), repo, err)
	}
	out := make([]runs.PullRequestRef, 0, len(found))
	for _, pr := range found {
		out = append(out, pullRequestRef(pr))
	}
	return out, nil
}

// pullRequestRef carries EVERY field of the receipt, Base above all: recovery
// locates a request by head branch alone, and without the base it would adopt
// one that merges somewhere nobody approved.
func pullRequestRef(pr forge.PullRequestRef) runs.PullRequestRef {
	return runs.PullRequestRef{
		Number: pr.Number, URL: pr.URL, HeadSHA: pr.HeadSHA, Base: pr.Base, State: pr.State,
	}
}

// intakeError types ResolveHead's failures for the HTTP layer.
//
// httpx maps errors by xerrors.ClassOf, which answers ClassInternal — a 500 —
// for anything that is not an *xerrors.Error. So a runs.OutcomeError here would
// turn a repository the operator mistyped, and a token that was revoked, into
// "internal error" with no detail. The two vocabularies exist for different
// consumers, and this adapter is the only place that knows which caller it is
// serving; collapsing them makes one consumer wrong by construction.
//
// An error that is already classified keeps its class: open's unsupported-forge
// refusal is a 400 already, and so is anything the assets layer answered.
func intakeError(op string, repo runs.RepoRef, err error) error {
	var xe *xerrors.Error
	if errors.As(err, &xe) {
		return err
	}
	switch {
	case errors.Is(err, forge.ErrNotFound):
		return xerrors.Wrap(xerrors.ClassNotFound,
			fmt.Sprintf("%s: the forge has no repository %q, or its branch %q", op, repo.Slug, repo.DefaultBranch), err)
	case errors.Is(err, forge.ErrUnauthorized):
		return xerrors.Wrap(xerrors.ClassInvalid,
			fmt.Sprintf("%s: the forge rejected the credential registered for %q", op, repo.Slug), err)
	case forge.IsRetryable(err):
		// The nearest honest answer in the problem taxonomy: come back later.
		// It covers a throttled forge, a 5xx and a failed dial alike — all of
		// them "not now", none of them the requester's mistake.
		return xerrors.Wrap(xerrors.ClassRateLimited,
			fmt.Sprintf("%s: the forge is not answering right now", op), err)
	}
	// Anything else really is ours: a decode failure, a vault blip. 500, and
	// the detail never leaves the process.
	return fmt.Errorf("%s: %w", op, err)
}

// publishError types the three RUN-PATH methods' failures onto the spine's
// outcome vocabulary.
//
// The classification lives HERE rather than in the client because retry is a
// run-level decision: platform reports what happened, and the composition root
// says what it means for a run.
//
// The default arm is TRANSIENT, and that is the whole point. These calls happen
// with a publication intent that is already 'pending' or 'pushed' — the branch
// may be live on the forge — and a permanent answer terminalizes the run, which
// no sweep revisits, orphaning that branch. So only what this adapter can PROVE
// permanent is permanent; everything else is "we could not see whether it
// happened", which unchargedWhileUnfinished downgrades to an uncharged,
// resumable retry bounded by the wall deadline.
//
// The four proven ones are each a human's problem rather than a retry:
//   - ErrConflict is the marker already carrying a request. publish_conflict is
//     load-bearing beyond the halt — openPublication branches on that exact code
//     to re-probe and adopt a request our own second driver opened.
//   - ErrUnauthorized is a dead credential. auth_expired is the code an operator
//     greps for; internal_error would bury a credential problem in a bug report.
//   - ErrForbidden is a live credential the forge will not let act — an
//     under-scoped token, an org enforcing SAML SSO, an archived repository.
//     It shares auth_expired deliberately: no code in the vocabulary separates
//     "the token is dead" from "the token is not allowed here", the operator's
//     move is the same one in both cases (reissue or re-authorize the
//     credential registered for this repository), and auth_expired is already
//     the code the halt set, the README's table and the run detail view carry
//     for it. What distinguishes them is the message, which quotes the forge's
//     own words. The alternative — repo_unavailable — would be worse: it says
//     the repository is gone, and here it demonstrably is not.
//   - ErrNotFound is a repository the forge will not show us: deleted, renamed,
//     or no longer visible to this token. Retrying cannot fix any of those.
//
// The thing to hold on to is that this arm is what keeps a permission failure
// OUT of the default arm. A 403 returned as a plain error is transient
// forge_unavailable, i.e. ~24 h of retries against a token that will never be
// allowed, ending in a settlement that blames an expiry after the push.
func publishError(op string, repo runs.RepoRef, err error) error {
	permanent := func(code, msg string) error {
		return runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: code, Message: fmt.Sprintf("%s: %s: %v", op, msg, err),
		}}
	}
	switch {
	case forge.IsRetryable(err):
		out := runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeForgeUnavailable,
			Message: fmt.Sprintf("%s: %v", op, err),
		}
		// A rate limit that named its own reset is the one case where the spine
		// should not guess: retryDelay takes a stated RetryAt verbatim, so
		// carrying it here is what turns "back off on a curve" into "come back
		// exactly when the forge said". Waking earlier would spend an attempt
		// into a window we were told is shut, and on a SECONDARY limit it
		// actively extends the lockout.
		if at, ok := forge.RetryAfter(err); ok {
			out.RetryAt = &at
		}
		return runs.OutcomeError{Outcome: out}
	case errors.Is(err, forge.ErrConflict):
		return permanent(runs.CodePublishConflict, "the forge refused as a conflict")
	case errors.Is(err, forge.ErrUnauthorized):
		return permanent(runs.CodeAuthExpired,
			fmt.Sprintf("the forge rejected the credential registered for %q", repo.Slug))
	case errors.Is(err, forge.ErrForbidden):
		return permanent(runs.CodeAuthExpired,
			fmt.Sprintf("the forge will not let the credential registered for %q do this", repo.Slug))
	case errors.Is(err, forge.ErrNotFound):
		return permanent(runs.CodeRepoUnavailable,
			fmt.Sprintf("the forge will not show repository %q", repo.Slug))
	}
	return runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomeTransient, Code: runs.CodeForgeUnavailable,
		Message: fmt.Sprintf("%s: %v", op, err),
	}}
}

// gitPublisher implements runs.GitPublisher over gitops.Publish (Plan 08
// Task 8).
//
// Each call gets its OWN directory and removes it on return. That is not
// hygiene: gitops.Publish clones into <dir>/publish and sets HOME=<dir>, so a
// second call on the same directory fails at `git clone` with a transient
// forge_unavailable — and nothing in gitops removes anything, so a shared
// directory would also leak a full clone plus a patch file per attempt.
//
// Both methods share prepare so ExpectedHead cannot disagree with PushPatch
// about repository, credential or inputs — the whole point of ExpectedHead is
// that it reproduces what PushPatch would do. ExpectedPublicationHead is NOT
// an offline call: it clones and runs ls-remote, so it needs the credential
// too. What it leaves alone is the remote's refs, not the network.
type gitPublisher struct {
	assets  *assets.Service
	workDir string
	// The configured clone roots. Without them cloneURL has no base to join a
	// slug onto, and with the WRONG ones a self-hosted run would query one
	// server and push to another.
	endpoints forgeEndpoints
}

func (g gitPublisher) prepare(ctx context.Context, req runs.PushRequest) (gitops.PublishInput, []string, error) {
	// Resolve (and validate) the forge before opening the credential: an
	// unsupported forge must fail without an audited secret use.
	url, err := cloneURL(g.endpoints, req.Repo.Forge, req.Repo.Slug)
	if err != nil {
		return gitops.PublishInput{}, nil, err
	}
	token, err := g.assets.UseCredential(ctx, req.Repo.CredentialID, "publish:"+req.Repo.Slug)
	if err != nil {
		return gitops.PublishInput{}, nil, err
	}
	authEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=" + cloneAuthHeader(string(token)),
	}
	// Every commit-shaping field is REPLAYED from the request, which the
	// caller filled from the durable intent row. Recomputing any of them —
	// the date above all — changes the rebuilt sha, misses the adopt
	// comparison, and turns a crash-after-push into a permanent conflict on
	// a branch that is genuinely ours.
	return gitops.PublishInput{
		CloneURL: url, BaseSHA: req.BaseSHA, Branch: req.Branch,
		TreeHash: req.TreeHash, Patch: req.Patch, Message: req.Message,
		AuthorName: req.AuthorName, AuthorEmail: req.AuthorEmail,
		Date: req.Date, ExpectHead: req.ExpectHead,
	}, authEnv, nil
}

// scratch materializes a directory this call owns outright.
func (g gitPublisher) scratch(ref runs.AttemptRef) (string, func(), error) {
	dir, err := os.MkdirTemp(g.workDir, "caesar-publish-")
	if err != nil {
		return "", nil, fmt.Errorf("app: publish scratch for %s: %w", ref, err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// notStarted types this composition root's OWN failures — the ones raised
// before any effect is attempted at all.
//
// gitops.AsOutcomeError covers everything gitops returns, and nothing else
// here was covered by anything. Resolving the clone URL, opening the
// credential and materializing the scratch directory all happen strictly
// BEFORE any effect, and so does resolving the forge client for a branch
// probe or a pull request. runs.ClassifyOutcome is default-deny: returned raw,
// a vault blip or a full disk becomes a PERMANENT internal_error, which
// terminalizes the run rather than retrying it. publish_not_started is the
// code minted for exactly this — uncharged, and honestly "it never began".
//
// An error that already carries its own outcome keeps it: only untyped ones
// need a verdict invented for them.
func notStarted(err error) error {
	var oe runs.OutcomeError
	if errors.As(err, &oe) {
		return err
	}
	return runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomeTransient, Code: runs.CodePublishNotStarted,
		Message: fmt.Sprintf("the publication could not be prepared: %v", err),
	}}
}

func (g gitPublisher) PushPatch(ctx context.Context, req runs.PushRequest) (runs.PushResult, error) {
	// scratch first, prepare second. The order is free — a doomed call spends
	// one mkdir and removes it — and it buys the only thing that makes the
	// scratch failure testable at all: it is now reachable without a
	// credential store standing behind prepare.
	dir, cleanup, err := g.scratch(req.Ref)
	if err != nil {
		return runs.PushResult{}, notStarted(err)
	}
	defer cleanup()
	in, authEnv, err := g.prepare(ctx, req)
	if err != nil {
		return runs.PushResult{}, notStarted(err)
	}
	rep, err := gitops.Publish(ctx, dir, in, authEnv)
	if err != nil {
		// gitops speaks its own TypedError; runs.ClassifyOutcome matches only
		// runs.OutcomeError and default-denies everything else to a permanent
		// internal_error — which would kill a run over a rejected push instead
		// of retrying it, and would never halt a conflicted marker for a human.
		return runs.PushResult{}, gitops.AsOutcomeError(err)
	}
	return runs.PushResult{HeadSHA: rep.HeadSHA, TreeHash: rep.TreeHash, Created: rep.Created}, nil
}

func (g gitPublisher) ExpectedHead(ctx context.Context, req runs.PushRequest) (string, error) {
	// Same order and same typing as PushPatch, and for the stronger reason:
	// the settlement that calls this arrives holding an intent that is already
	// 'pending' or 'pushed', so a permanent answer here would terminalize a
	// run over a branch that may be live on the forge.
	dir, cleanup, err := g.scratch(req.Ref)
	if err != nil {
		return "", notStarted(err)
	}
	defer cleanup()
	in, authEnv, err := g.prepare(ctx, req)
	if err != nil {
		return "", notStarted(err)
	}
	head, err := gitops.ExpectedPublicationHead(ctx, dir, in, authEnv)
	return head, gitops.AsOutcomeError(err)
}

// principalSource implements runs.PrincipalSource over identity. Four lines,
// in the composition root, because the runs context never learns what a user
// row looks like.
type principalSource struct{ svc *identity.Service }

func (p principalSource) Email(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := p.svc.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}
