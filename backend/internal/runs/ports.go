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

// Forge resolves a repo's default branch to the commit a run pins as its
// base SHA. Plan 08 grows this port into the publish state machine.
type Forge interface {
	ResolveHead(ctx context.Context, repo RepoRef) (string, error)
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
