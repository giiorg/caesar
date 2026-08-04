package runs

import (
	"context"
	"fmt"
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
	ID   uuid.UUID
	Kind string
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

// ExecutionRequest is the spine's precursor of Plan 06's RunSpec:
// explicit inputs, never ambient state (spec §3 artifact flow).
type ExecutionRequest struct {
	Ref          AttemptRef
	Kind         string
	HarnessID    uuid.UUID
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
