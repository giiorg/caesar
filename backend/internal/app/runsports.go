package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
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

// unavailableForge refuses task intake honestly until Plan 08's forge
// adapters exist: no run can pin a base SHA, so no task can be created
// through production wiring yet (stubs never fake success).
type unavailableForge struct{}

func (unavailableForge) ResolveHead(context.Context, runs.RepoRef) (string, error) {
	return "", xerrors.Wrap(xerrors.ClassInvalid, "forge adapters arrive in Plan 08; task intake is not yet available in production wiring", xerrors.ErrNotImplemented)
}

// The publish half of the port. Plan 12 replaces the whole type with a real
// forge adapter; until then every method refuses the same way ResolveHead
// does.
//
// Returning an ERROR rather than ("", false, nil) is deliberate: a stub that
// answered "the branch does not exist" would let recoverBranch proceed to a
// push against a host nobody has looked at (stubs never fake success).
func (unavailableForge) BranchHead(context.Context, runs.RepoRef, string) (string, bool, error) {
	return "", false, xerrors.Wrap(xerrors.ClassInvalid, "forge adapters arrive in Plan 12; branch probes are not yet available in production wiring", xerrors.ErrNotImplemented)
}

func (unavailableForge) OpenPullRequest(context.Context, runs.RepoRef, runs.PullRequestSpec) (runs.PullRequestRef, error) {
	return runs.PullRequestRef{}, xerrors.Wrap(xerrors.ClassInvalid, "forge adapters arrive in Plan 12; pull requests are not yet available in production wiring", xerrors.ErrNotImplemented)
}

func (unavailableForge) FindPullRequests(context.Context, runs.RepoRef, string) ([]runs.PullRequestRef, error) {
	return nil, xerrors.Wrap(xerrors.ClassInvalid, "forge adapters arrive in Plan 12; pull requests are not yet available in production wiring", xerrors.ErrNotImplemented)
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
}

func (g gitPublisher) prepare(ctx context.Context, req runs.PushRequest) (gitops.PublishInput, []string, error) {
	// Resolve (and validate) the forge before opening the credential: an
	// unsupported forge must fail without an audited secret use.
	url, err := cloneURL(req.Repo.Forge, req.Repo.Slug)
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

func (g gitPublisher) PushPatch(ctx context.Context, req runs.PushRequest) (runs.PushResult, error) {
	in, authEnv, err := g.prepare(ctx, req)
	if err != nil {
		return runs.PushResult{}, err
	}
	dir, cleanup, err := g.scratch(req.Ref)
	if err != nil {
		return runs.PushResult{}, err
	}
	defer cleanup()
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
	in, authEnv, err := g.prepare(ctx, req)
	if err != nil {
		return "", err
	}
	dir, cleanup, err := g.scratch(req.Ref)
	if err != nil {
		return "", err
	}
	defer cleanup()
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
