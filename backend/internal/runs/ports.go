package runs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// AttemptRef is the execution identity (spec §2): everything an attempt
// touches is named by it.
type AttemptRef struct {
	RunID   uuid.UUID
	NodeID  string
	Attempt int32
}

func (r AttemptRef) String() string {
	return fmt.Sprintf("caesar-%s-%s-%d", r.RunID, r.NodeID, r.Attempt)
}

// attemptRefPrefix and uuidLen are what make the format reversible: the run id
// is fixed-width, so the node id — which routinely carries dashes — starts at a
// known offset, and only the LAST dash separates it from the attempt ordinal.
const (
	attemptRefPrefix = "caesar-"
	uuidLen          = 36
)

// ParseAttemptRef is the inverse of AttemptRef.String(). Recovery needs it:
// a credential lease records its owner as the string alone, and the stale-lease
// janitor has to resolve that back to a phase before it may act on it.
//
// Anything that is not a ref this build wrote is refused rather than guessed
// at — a lease held by something else entirely (an operator's manual hold, a
// future subsystem) must be left alone, not reconciled against a phase that
// does not exist.
func ParseAttemptRef(s string) (AttemptRef, error) {
	rest, ok := strings.CutPrefix(s, attemptRefPrefix)
	if !ok || len(rest) < uuidLen+1 || rest[uuidLen] != '-' {
		return AttemptRef{}, fmt.Errorf("runs: %q is not an attempt ref", s)
	}
	runID, err := uuid.Parse(rest[:uuidLen])
	if err != nil {
		return AttemptRef{}, fmt.Errorf("runs: attempt ref %q: %w", s, err)
	}
	tail := rest[uuidLen+1:]
	cut := strings.LastIndexByte(tail, '-')
	if cut <= 0 { // no ordinal, or an empty node id
		return AttemptRef{}, fmt.Errorf("runs: attempt ref %q has no node id and attempt", s)
	}
	attempt, err := strconv.ParseInt(tail[cut+1:], 10, 32)
	if err != nil || attempt < 1 {
		return AttemptRef{}, fmt.Errorf("runs: attempt ref %q has no valid attempt ordinal", s)
	}
	return AttemptRef{RunID: runID, NodeID: tail[:cut], Attempt: int32(attempt)}, nil
}

// PinnedDefinition is what a run pins at intake: the version identity
// plus the runs-owned mirror of its document.
type PinnedDefinition struct {
	VersionID   uuid.UUID
	WorkflowID  uuid.UUID
	Version     int32
	ContentHash string
	Doc         DefinitionDoc
}

// RepoRef and HarnessRef are the spine's views of peer-context assets.
type RepoRef struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	CredentialID  uuid.UUID
	Forge         string
	Slug          string
	DefaultBranch string
}

type HarnessRef struct {
	ID             uuid.UUID
	Kind           string
	CredentialID   uuid.UUID
	CredentialKind string
}

// CredentialBinding is the phase's pinned credential identity (spec §3):
// resolved once at request build, it travels intact to the runner, which must
// never re-resolve the credential off the mutable harness row.
type CredentialBinding struct {
	ID   uuid.UUID
	Kind string // assets credential kind vocabulary, opaque here
}

// The five ports the spine consumes. Contexts never import each other:
// app implements these over workflows.Service / assets.Service and the
// fake package (spec §1 hexagonal boundaries).
type DefinitionSource interface {
	// Definition resolves (workflow, version) coordinates at task intake.
	Definition(ctx context.Context, workflowID uuid.UUID, version int32) (PinnedDefinition, error)
	// DefinitionByVersionID resolves the version a task already pinned
	// (re-runs and the orchestrator's plan loading).
	DefinitionByVersionID(ctx context.Context, versionID uuid.UUID) (PinnedDefinition, error)
}

type RepoSource interface {
	Repo(ctx context.Context, id uuid.UUID) (RepoRef, error)
}

type HarnessSource interface {
	HarnessByName(ctx context.Context, name string) (HarnessRef, error)
}

// Forge is the control plane's view of a code host. Plan 05 needed one
// method; the publish state machine needs three more, and every one of them
// is either an idempotency probe or an effect that records a receipt.
//
// EVERY implementation must return OutcomeError from BranchHead,
// OpenPullRequest and FindPullRequests — the same rule gitops.AsOutcomeError
// states for GitPublisher, and for the same reason. ClassifyOutcome matches
// OutcomeError and nothing else, and is default-deny: an untyped error becomes
// a PERMANENT internal_error. All three are called only from the publish state
// machine, which stands inside an intent that is 'pending' or 'pushed', so a
// permanent answer terminalizes the run — and no sweep revisits a failed run,
// so a branch already on the forge becomes an orphan. Only what the
// implementation can PROVE permanent may be permanent; everything else is
// transient, which unchargedWhileUnfinished then downgrades to an uncharged,
// resumable retry.
//
// ResolveHead is the exception and is deliberately NOT in that rule: its only
// callers are task intake, and its error reaches the HTTP layer, which
// classifies by xerrors and answers 500 for anything else. A repository the
// operator mistyped must read as the requester's mistake, not as caesar's.
type Forge interface {
	// ResolveHead resolves a repo's default branch to the commit a run pins
	// as its base SHA.
	ResolveHead(ctx context.Context, repo RepoRef) (string, error)
	// BranchHead reports what a branch points at; ok is false when it does
	// not exist. Recovery's first question.
	BranchHead(ctx context.Context, repo RepoRef, branch string) (string, bool, error)
	// OpenPullRequest creates the pull/merge request and returns its receipt.
	OpenPullRequest(ctx context.Context, repo RepoRef, spec PullRequestSpec) (PullRequestRef, error)
	// FindPullRequests lists every pull/merge request on a head branch. This
	// is what turns "the receipt was lost" into "it already happened" — but
	// only after the caller proves one of the candidates is the publication
	// the approval authorized. A head branch alone does not prove it.
	FindPullRequests(ctx context.Context, repo RepoRef, headBranch string) ([]PullRequestRef, error)
}

type PullRequestSpec struct{ Head, Base, Title, Body string }

type PullRequestRef struct {
	Number  int64
	URL     string
	HeadSHA string
	// Base is the branch this pull request merges INTO. It is carried because
	// recovery locates a pull request by HEAD branch alone, and a head branch
	// is not a unique description of a publication: a human or a bot can open
	// a second request from our marker to a different target. Without the
	// base, recovery would adopt it and report a publication that merges
	// somewhere nobody approved.
	Base  string
	State string
}

// GitPublisher performs the git half of a publication: a fresh trusted
// checkout at BaseSHA, the approved patch applied, a commit with the pinned
// attribution, and a push under an expected-remote-SHA precondition.
//
// It is a port rather than a direct call so the golden set can inject a
// crash between the push and its receipt — the one window that makes a
// publication non-idempotent if the machine gets it wrong.
type GitPublisher interface {
	PushPatch(ctx context.Context, req PushRequest) (PushResult, error)
	// ExpectedHead reports the commit sha PushPatch WOULD produce, touching
	// no ref. Task 10's expired-run settlement needs to identify a branch
	// whose push receipt was lost while holding no authority to write
	// anything; the pinned commit date is what makes the answer exact.
	//
	// Every field PushPatch would have used must be present, TreeHash
	// included: an empty tree hash is a permanent invalid_output refused
	// before the clone, not an opt-out.
	ExpectedHead(ctx context.Context, req PushRequest) (string, error)
}

type PushRequest struct {
	Ref                     AttemptRef
	Repo                    RepoRef
	Branch                  string
	BaseSHA                 string
	TreeHash                string
	Patch                   string // base64(std) canonical diff, the artifact wire form
	Message                 string
	AuthorName, AuthorEmail string
	// Date is the intent's created_at, pinned into both git identities so the
	// commit sha is a pure function of this request. It is durable and stable
	// across attempts, which is what lets a retry after a lost push receipt
	// rebuild the SAME commit and adopt the branch instead of force-pushing a
	// replacement over it. Never the wall clock.
	Date time.Time
	// ExpectHead is "" when the branch must not exist yet.
	ExpectHead string
	Deadline   time.Time
}

type PushResult struct {
	HeadSHA  string
	TreeHash string
	Created  bool
}

// PublicationBranch is the deterministic marker (spec §3): derived from the
// approved artifact id, which is a uuidv7 and therefore unique per node per
// generation. Recovery reconstructs it from the authorization row alone.
func PublicationBranch(artifactID uuid.UUID) string {
	return "caesar/" + artifactID.String()
}

// PrincipalSource resolves a user id to the identity a publication is
// attributed to (spec §6: "the requester's identity flows into audit, run
// records, and published commits/PRs"). app implements it over identity;
// the runs context never learns what a user row looks like.
type PrincipalSource interface {
	Email(ctx context.Context, userID uuid.UUID) (string, error)
}

// Executor runs one phase attempt to a typed result. Plan 06's Docker
// runner implements it for real; fake.Executor scripts it; and
// NotImplementedExecutor is the honest production stub until then.
type Executor interface {
	Execute(ctx context.Context, req ExecutionRequest) (ExecutionResult, error)
	Inspect(ctx context.Context, ref AttemptRef) (InspectResult, error)
}

// CredentialReconciler settles an attempt's credential obligation and nothing
// else: take custody, stop whatever is still running, sync the credential back
// with its receipt, release the lease (spec §2.8). It is deliberately NOT
// Inspect — the janitor paths that call it have already decided the attempt's
// outcome, and an adoption here could evaluate a finished attempt as a success
// the run is no longer entitled to.
//
// The Docker runner implements it; executors that hold no credentials (the
// fake, the not-implemented stub) implement it as a no-op.
type CredentialReconciler interface {
	ReconcileCredential(ctx context.Context, ref AttemptRef) error
}

// StaleLease is one credential lease whose owner stopped heartbeating. The
// credential id travels with it because the janitor logs against it; the
// attempt ref is the string the lease row holds, parsed back with
// ParseAttemptRef.
type StaleLease struct {
	CredentialID uuid.UUID
	AttemptRef   string
}

// StaleLeaseSource surfaces leases nobody is keeping alive. Expiry alone never
// releases one (spec §2.1: only a concluded reconciliation does), so these are
// handed to reconciliation and never to a new acquirer. app adapts it over
// assets — the runs context never imports that vocabulary.
type StaleLeaseSource interface {
	StaleCredentialLeases(ctx context.Context, olderThan time.Duration) ([]StaleLease, error)
}

// ExecutionRequest is the spine's precursor of Plan 06's RunSpec:
// explicit inputs, never ambient state (spec §3 artifact flow).
type ExecutionRequest struct {
	Ref       AttemptRef
	Kind      string
	HarnessID uuid.UUID
	// Credential is the binding pinned at build time; its zero value is a
	// harnessless node, which reaches no provider and needs no secret.
	Credential   CredentialBinding
	Repo         RepoRef
	Instructions string
	Inputs       []ArtifactInput
	TaskInputs   map[string]any
	BaseSHA      string
	Deadline     time.Time
}

type ArtifactInput struct {
	ID      uuid.UUID
	Type    string
	Payload map[string]any
}

// NewArtifact is an executor-produced work product; the orchestrator
// stamps identity and lineage when persisting.
type NewArtifact struct {
	Type    string
	Payload map[string]any
}

type ExecutionResult struct {
	Artifacts []NewArtifact
}

// InspectResult reports what the runtime knows about an attempt during
// reconciliation: a completed execution with intact outputs is adopted
// idempotently; anything else is kill-then-inspect (spec §3).
type InspectResult struct {
	Completed bool
	Result    ExecutionResult
	// Outcome is the adopted attempt's typed FAILURE, when it has one: a
	// harness that finished and said why (a cap window with its reset, a dead
	// credential) is adopted as that answer rather than degraded to
	// reconcile_lost. nil means the completion succeeded; Result carries it.
	Outcome *Outcome
}

// NotImplementedExecutor is wired in production serve until Plan 06: a
// claimed phase dies fast with a typed permanent outcome. Stubs never
// fake success (decisions §6.13).
type NotImplementedExecutor struct{}

func (NotImplementedExecutor) Execute(context.Context, ExecutionRequest) (ExecutionResult, error) {
	return ExecutionResult{}, OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: CodeNotImplemented,
		Message: xerrors.NotImplemented("runs: executor (Plan 06)").Error(),
	}}
}

func (NotImplementedExecutor) Inspect(context.Context, AttemptRef) (InspectResult, error) {
	return InspectResult{}, OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: CodeNotImplemented,
		Message: xerrors.NotImplemented("runs: executor inspect (Plan 06)").Error(),
	}}
}

// Admission gates claiming on runner capacity (spec §8: the runner
// refuses new claims under a free-space watermark — typed backpressure,
// checked BEFORE a claim burns an attempt's lease).
type Admission interface {
	AdmitClaim(ctx context.Context) error
}

// AlwaysAdmit is the no-runner posture (and the fake-driven tests').
type AlwaysAdmit struct{}

func (AlwaysAdmit) AdmitClaim(context.Context) error { return nil }
