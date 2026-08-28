// Package api is the execution spine's HTTP surface. DTO structs here
// are the source the OpenAPI document is generated from (Go types →
// contract, spec §4).
package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

type CreateTaskRequest struct {
	RepoID          string         `json:"repo_id"`
	WorkflowID      string         `json:"workflow_id"`
	WorkflowVersion int32          `json:"workflow_version"`
	Title           string         `json:"title"`
	Inputs          map[string]any `json:"inputs,omitempty"`
}

// TaskResponse projects a task with its newest run: Status is that run's
// status, which is what a task list renders.
//
// EVERY property is required. `taskResponse` sets all ten unconditionally and
// none carries omitempty, so there is no shape of this struct in which a key is
// absent — and an unmarked property renders optional in TypeScript, which makes
// the Tasks screen branch on a case the server cannot produce. Inputs is the
// only nullable one: a Go nil map marshals to null and it is a map rather than a
// $ref, so the generator really does render ["object","null"] beside the
// required entry.
type TaskResponse struct {
	ID                string         `json:"id" required:"true"`
	ProjectID         string         `json:"project_id" required:"true"`
	RepoID            string         `json:"repo_id" required:"true"`
	WorkflowVersionID string         `json:"workflow_version_id" required:"true"`
	Title             string         `json:"title" required:"true"`
	Inputs            map[string]any `json:"inputs" required:"true"`
	RequesterID       string         `json:"requester_id" required:"true"`
	Status            string         `json:"status" required:"true"`
	LatestRunID       string         `json:"latest_run_id" required:"true"`
	CreatedAt         time.Time      `json:"created_at" required:"true"`
}

type TaskListResponse struct {
	Tasks []TaskResponse `json:"tasks" required:"true"`
}

// RunResponse carries phases, artifact metadata and publications inline on
// the single-run read; list entries leave all three empty.
//
// EVERY property here is required except the three whose schema is a `$ref` to
// a named component AND whose value is genuinely null on ordinary runs —
// outcome on any live run, decision and evidence on a phase. This generator
// gives a `$ref` property no null branch at all (jsonschema-go only envelops
// nullability when the reflector asks it to, and contracts.go builds a bare
// reflector), so `required` on one of those is a claim of NON-nullability that
// would render `outcome: ApiOutcomeResponse` in TypeScript — not optional, not
// nullable, and wrong on every run that has not ended. Scalars and slices are
// unaffected: they really do render ["type","null"] while staying required.
type RunResponse struct {
	ID     string `json:"id" required:"true"`
	TaskID string `json:"task_id" required:"true"`
	// The three labels a run page's header renders. They live only on the
	// task, the repository and the workflow behind the pinned version, so a
	// client without them is one fetch per label away from a title.
	TaskTitle      string              `json:"task_title" required:"true"`
	RepoSlug       string              `json:"repo_slug" required:"true"`
	WorkflowName   string              `json:"workflow_name" required:"true"`
	Status         string              `json:"status" required:"true"`
	DefinitionHash string              `json:"definition_hash" required:"true"`
	BaseSHA        string              `json:"base_sha" required:"true"`
	PolicySnapshot runs.PolicySnapshot `json:"policy_snapshot" required:"true"`
	// Generation is the revision round the run is on: 1 until a gate sends
	// work back, one higher per round after that.
	Generation      int32     `json:"generation" required:"true"`
	ExecutionUsedMS int64     `json:"execution_used_ms" required:"true"`
	WallDeadline    time.Time `json:"wall_deadline" required:"true"`
	// Outcome.Class on a terminal run can still read "transient" when the run
	// died parked past its deadline over a settled publication. That is the
	// orchestrator's classification, not this projection's — it predates the
	// gate surface and is routed to Task 14; nothing here makes it worse.
	//
	// Deliberately NOT required: it is a $ref and it is null on every run that
	// has not ended. See the type comment.
	Outcome      *OutcomeResponse      `json:"outcome"`
	CreatedAt    time.Time             `json:"created_at" required:"true"`
	StartedAt    *time.Time            `json:"started_at" required:"true"`
	FinishedAt   *time.Time            `json:"finished_at" required:"true"`
	Phases       []PhaseResponse       `json:"phases" required:"true"`
	Artifacts    []ArtifactResponse    `json:"artifacts" required:"true"`
	Publications []PublicationResponse `json:"publications" required:"true"`
}

type RunListResponse struct {
	Runs []RunResponse `json:"runs" required:"true"`
}

// RunListPageResponse is one page. next_cursor is required AND nullable: null
// means "this was the last page", which is a value the client branches on, not
// a key it fails to find.
type RunListPageResponse struct {
	Runs       []RunSummaryResponse `json:"runs" required:"true"`
	NextCursor *string              `json:"next_cursor" required:"true"`
}

// RunSummaryResponse is a run as a LIST ROW, and it is a different projection
// from RunResponse on purpose: everything a Home or Tasks card renders is here,
// so the card costs no second request. Reusing the reduced RunResponse — no
// task title, no repo or workflow label, no phase summary — is what makes a
// list of cards an N+1.
type RunSummaryResponse struct {
	ID          string `json:"id" required:"true"`
	TaskID      string `json:"task_id" required:"true"`
	TaskTitle   string `json:"task_title" required:"true"`
	ProjectID   string `json:"project_id" required:"true"`
	ProjectName string `json:"project_name" required:"true"`
	RepoSlug    string `json:"repo_slug" required:"true"`
	// WorkflowName is the workflow's CURRENT name, resolved through the version
	// the task pinned. A rename is therefore visible on historical runs, which
	// is the honest reading: the workflow is the thing that was renamed.
	WorkflowName string `json:"workflow_name" required:"true"`
	Status       string `json:"status" required:"true"`
	// Attempt is the newest attempt ordinal among the run's materialized
	// phases — what a card means by "attempt 2". It is 0 on a run nothing has
	// materialized yet, because a run has no attempt of its own.
	Attempt     int32  `json:"attempt" required:"true"`
	Generation  int32  `json:"generation" required:"true"`
	RequesterID string `json:"requester_id" required:"true"`
	// Three real columns and no synthesized "updated_at": a card renders
	// finished_at ?? started_at ?? created_at, and the choice stays visible.
	CreatedAt  time.Time  `json:"created_at" required:"true"`
	StartedAt  *time.Time `json:"started_at" required:"true"`
	FinishedAt *time.Time `json:"finished_at" required:"true"`
	// PhaseSummary is the run's whole SHAPE — one entry per node of the
	// compiled definition, in topological order, materialized or not. A scalar
	// "current phase" cannot render a row of dots, and a list that grew as the
	// run advanced would draw a two-node run and a ten-node run identically.
	PhaseSummary []PhaseSummaryResponse `json:"phase_summary" required:"true"`
}

// PhaseSummaryResponse is one dot in a card's phase row.
type PhaseSummaryResponse struct {
	NodeID string `json:"node_id" required:"true"`
	// NodeType rides along because a gate dot is drawn differently from a work
	// dot, and a client must not infer a node's type from its id string.
	NodeType string `json:"node_type" required:"true"`
	// Status is required AND nullable, and null is not "pending": `pending` is
	// a real phase status meaning "materialized and claimable", while null
	// means the orchestrator has not reached this node at all — a dim dot, not
	// a queued one.
	Status     *string `json:"status" required:"true"`
	Attempt    int32   `json:"attempt" required:"true"`
	Generation int32   `json:"generation" required:"true"`
}

// ListRunsQuery is the query string, declared as a struct so the parameters
// reach the contract. Every field is `query`-tagged, so the reflector renders
// parameters and no request body.
type ListRunsQuery struct {
	Status string `query:"status" description:"pending | running | waiting | succeeded | failed | superseded | dead"`
	TaskID string `query:"task_id" description:"only this task's runs (uuid)"`
	// WorkflowID is the WORKFLOW and not a version of it, which the description
	// says outright: the two are both uuids, so a caller who passes the wrong
	// one gets an empty page and no other signal.
	WorkflowID string `query:"workflow_id" description:"only runs of tasks pinned to any version of this workflow (uuid)"`
	RepoID     string `query:"repo_id" description:"only runs of tasks on this repo (uuid)"`
	Cursor     string `query:"cursor" description:"opaque; the previous page's next_cursor"`
	Limit      int32  `query:"limit" description:"1-200, default 50"`
}

type OutcomeResponse struct {
	Class   string     `json:"class"`
	Code    string     `json:"code"`
	Message string     `json:"message"`
	RetryAt *time.Time `json:"retry_at,omitempty"`
}

type PhaseResponse struct {
	NodeID      string `json:"node_id" required:"true"`
	Attempt     int32  `json:"attempt" required:"true"`
	MaxAttempts int32  `json:"max_attempts" required:"true"`
	Status      string `json:"status" required:"true"`
	Generation  int32  `json:"generation" required:"true"`
	// Outcome is null on every phase that has not ended, and it is a $ref —
	// which this generator gives no null branch. Required would render it
	// non-nullable in TypeScript; optional is the honest half-truth, because it
	// makes the client narrow before it reads. Same reasoning as Evidence.
	Outcome    *OutcomeResponse `json:"outcome"`
	CreatedAt  time.Time        `json:"created_at" required:"true"`
	FinishedAt *time.Time       `json:"finished_at" required:"true"`
	// Decision is present only on a gate attempt somebody decided. A revision
	// leaves two gate attempts on the same node id, each carrying its own. Not
	// required, for the same $ref reason as Outcome and Evidence.
	Decision *GateDecisionSummary `json:"decision"`
	// Evidence is present only on a gate attempt still AWAITING a decision, so
	// it and Decision are never both set. It is deliberately not marked
	// required: the property really is null on every non-gate phase and on
	// every decided one, and the generator cannot express "this object or null"
	// for a named schema — a $ref property carries no null branch (see
	// jsonschema-go's checkNullability, which only envelops when the reflector
	// asks it to). Marking it required would tell openapi-typescript the object
	// is always there, which is the one thing that is not true.
	Evidence *GateEvidenceResponse `json:"evidence"`
}

// Every property is required and none is a $ref, so the "a required $ref claims
// non-nullability" rule does not bite here: the server sends all seven on every
// run projection. parent_ids keeps its null branch because a Go slice can
// marshal to null — but parentIDStrings builds with make, so a client will
// satisfy that branch and never observe one.
type ArtifactResponse struct {
	ID         string    `json:"id" required:"true"`
	NodeID     string    `json:"node_id" required:"true"`
	Attempt    int32     `json:"attempt" required:"true"`
	Generation int32     `json:"generation" required:"true"`
	Type       string    `json:"type" required:"true"`
	ParentIDs  []string  `json:"parent_ids" required:"true"`
	CreatedAt  time.Time `json:"created_at" required:"true"`
}

// ArtifactPayloadResponse is one artifact WITH its content. The metadata
// duplicates ArtifactResponse deliberately: this endpoint is reachable without
// having fetched the run, and a client that had to correlate two responses to
// learn an artifact's type would be one join away from rendering a plan as a
// diff.
//
// Every field is required and none is a $ref, so the "a required $ref claims
// non-nullability" rule does not bite here: the server sends all nine on every
// 200. The generator still gives parent_ids and payload a null branch, because
// a Go slice and a Go map can each marshal to null — but neither is ever null
// FROM THIS ENDPOINT, and both are ENFORCED rather than assumed:
// parentIDStrings builds with make, and ArtifactPayload normalizes a nil map to
// an empty one. That second guard is not theoretical — a payload column holding
// the jsonb literal `null` decodes to a nil map, and a harness that declares an
// artifact with no payload key writes exactly that. A client must satisfy the
// null branch to typecheck; it will not observe one.
type ArtifactPayloadResponse struct {
	ID         string    `json:"id" required:"true"`
	RunID      string    `json:"run_id" required:"true"`
	NodeID     string    `json:"node_id" required:"true"`
	Attempt    int32     `json:"attempt" required:"true"`
	Generation int32     `json:"generation" required:"true"`
	Type       string    `json:"type" required:"true"`
	ParentIDs  []string  `json:"parent_ids" required:"true"`
	CreatedAt  time.Time `json:"created_at" required:"true"`
	// Payload is the artifact's content verbatim, in the shape its producer
	// wrote. A `patch` carries `diff` base64-encoded (collect.go) — decoding is
	// the client's, and decoding TWICE is how a diff becomes garbage.
	Payload map[string]any `json:"payload" required:"true"`
}

type GateDecisionRequest struct {
	Decision string `json:"decision"` // approve | request_changes | reject
	Note     string `json:"note,omitempty"`
}

type GateDecisionResponse struct {
	DecisionID string `json:"decision_id"`
	Decision   string `json:"decision"`
	RunStatus  string `json:"run_status"`
	// Generation is the generation this response is about, and which one that
	// is depends on the decision: approve and reject report the generation you
	// just DECIDED, request_changes reports the NEW generation your revision
	// opened. One documented field beats two where one is always null.
	Generation int32 `json:"generation"`
	// AuthorizationID is empty when the gate authorizes no publish node.
	AuthorizationID string `json:"authorization_id,omitempty"`
}

// ApprovalResponse is one attention item. It carries everything a list view
// renders so the inbox is one request, not one plus N.
//
// required:"true" is read by swaggest/jsonschema-go and puts the property in
// the schema's `required` array; without it openapi-typescript renders it
// optional. Only Plan 11's web slice is marked up — the rest of this file's
// DTOs are Plan 12's to do as those screens arrive.
type ApprovalResponse struct {
	RunID        string    `json:"run_id" required:"true"`
	TaskID       string    `json:"task_id" required:"true"`
	ProjectID    string    `json:"project_id" required:"true"`
	TaskTitle    string    `json:"task_title" required:"true"`
	NodeID       string    `json:"node_id" required:"true"`
	Attempt      int32     `json:"attempt" required:"true"`
	Generation   int32     `json:"generation" required:"true"`
	RequesterID  string    `json:"requester_id" required:"true"`
	WaitingSince time.Time `json:"waiting_since" required:"true"`
	// Evidence is always present: every row of this list is a gate awaiting a
	// decision, which is exactly the state that has evidence.
	Evidence GateEvidenceResponse `json:"evidence" required:"true"`
}

// GateEvidenceResponse is one gate's decision surface, answered server-side.
// It is the SAME shape on a list row and on a run's gate phase because §3.4 and
// §3.5 are one component with two hosts; a second shape would be a second
// renderer, and the two would drift.
type GateEvidenceResponse struct {
	// ExaminedArtifactID is empty ONLY where the pinned definition binds an
	// artifact the run never produced — a state decideGate also refuses. The
	// surface says the evidence is missing; it does not render an empty diff.
	// CanDecide is false there for every caller, so a client never has to infer
	// undecidability from an empty string.
	ExaminedArtifactID string `json:"examined_artifact_id" required:"true"`
	// A plan and a patch render differently and the client must know which
	// before it fetches the payload.
	ExaminedArtifactType string `json:"examined_artifact_type" required:"true"`
	// Required AND nullable. "This gate has no verdict" is a value the client
	// handles, not a key it fails to find.
	SummaryArtifactID *string `json:"summary_artifact_id" required:"true"`
	// CanDecide covers every way decideGate refuses before it reads the
	// decision, and they are independent: this endpoint lists gates for any
	// grant while decideGate needs approve, the run may be past its wall
	// deadline, the run's policy may forbid the requester deciding their own
	// gate, and a gate whose examined artifact the run never produced is
	// decidable by nobody.
	// Where it is false the client suppresses ALL THREE controls — DecideGate
	// refuses before it branches on decision type.
	CanDecide bool `json:"can_decide" required:"true"`
	// One of "", "not_approver", "deadline_expired", "self_approval_forbidden",
	// "evidence_missing". Empty exactly when CanDecide is true. Computed
	// server-side in decideGate's own order, so it names the refusal the caller
	// would actually receive.
	CannotDecideReason string `json:"cannot_decide_reason" required:"true"`
	// WallDeadline rides along because the deadline refusal is displayable
	// BEFORE it bites: a run past its deadline can no longer be decided and the
	// deadline is on the run, so a card can say "this expires in 20 minutes"
	// instead of only reporting the 409 afterwards.
	//
	// It ALSO stays on the wire now that CanDecide accounts for it, and the two
	// answer different questions. CanDecide is the deadline as of the DATABASE
	// clock at the instant this response was composed — the same clock the
	// decision transaction reads — while this field is what lets a client that
	// has held the response open notice the deadline passing afterwards. A
	// client that suppressed on its own clock ALONE would make the refusal a
	// function of the reader's machine; one that trusted CanDecide alone would
	// keep offering controls on a response that has since gone stale.
	WallDeadline time.Time `json:"wall_deadline" required:"true"`
}

// Approvals is required AND nullable: the property is always present in the
// contract, and its type stays ["array","null"] because a nil slice really
// does marshal to null. The two are independent.
type ApprovalListResponse struct {
	Approvals []ApprovalResponse `json:"approvals" required:"true"`
}

// ListDecisionsQuery is the query string, declared as a struct so the
// parameters reach the contract. Every field is `query`-tagged, so the
// reflector renders parameters and no request body.
type ListDecisionsQuery struct {
	Cursor string `query:"cursor" description:"opaque; the previous page's next_cursor"`
	Limit  int32  `query:"limit" description:"1-200, default 50"`
}

// DecisionPageResponse is one page of decision history. next_cursor is required
// AND nullable: null means "this was the last page", which is a value the
// client branches on, not a key it fails to find.
type DecisionPageResponse struct {
	Decisions  []DecisionResponse `json:"decisions" required:"true"`
	NextCursor *string            `json:"next_cursor" required:"true"`
}

// DecisionResponse is one settled gate as a HISTORY ROW — the counterpart to
// ApprovalResponse, which is one still waiting. It is a different projection
// from GateDecisionSummary because that one hangs off a phase of a run already
// on screen, while this one crosses runs and must say which run and which task
// it came from.
//
// GENERATION AND ATTEMPT ARE BOTH HERE AND BOTH LOAD-BEARING. Task 18's rows
// link with (run_id, node_id, generation, attempt) — Task 9's selector triple —
// because a revision leaves two decided gate attempts under one node id, and a
// link carrying only the generation lands on whichever attempt is newest rather
// than on the one that was decided. The attempt exists nowhere on the decision
// row itself; the server recovers it through the phase row the decision keys.
//
// self_approval is deliberately absent, unlike GateDecisionSummary: it is a
// property of the decision as an AUDIT fact, and the history tab is a
// navigation surface. Task 23's admin audit view is where that belongs.
type DecisionResponse struct {
	ID                 string    `json:"id" required:"true"`
	RunID              string    `json:"run_id" required:"true"`
	TaskID             string    `json:"task_id" required:"true"`
	TaskTitle          string    `json:"task_title" required:"true"`
	NodeID             string    `json:"node_id" required:"true"`
	Generation         int32     `json:"generation" required:"true"`
	Attempt            int32     `json:"attempt" required:"true"`
	Decision           string    `json:"decision" required:"true"`
	Note               string    `json:"note" required:"true"`
	ActedByEmail       string    `json:"acted_by_email" required:"true"`
	ActedAt            time.Time `json:"acted_at" required:"true"`
	ExaminedArtifactID string    `json:"examined_artifact_id" required:"true"`
}

// decisionPageResponse projects one page. next is the service's empty string
// for "there is no next page", and it becomes a JSON null rather than an absent
// key or an empty string: the client branches on the value, and "" is a cursor
// shape it would have to know is special.
func decisionPageResponse(list []runs.DecidedGate, next string) DecisionPageResponse {
	out := DecisionPageResponse{Decisions: make([]DecisionResponse, 0, len(list))}
	if next != "" {
		out.NextCursor = &next
	}
	for _, d := range list {
		out.Decisions = append(out.Decisions, DecisionResponse{
			ID: d.ID.String(), RunID: d.RunID.String(), TaskID: d.TaskID.String(),
			TaskTitle: d.TaskTitle, NodeID: d.NodeID,
			Generation: d.Generation, Attempt: d.Attempt,
			Decision: d.Decision, Note: d.Note,
			ActedByEmail: d.ActedByEmail, ActedAt: d.ActedAt,
			ExaminedArtifactID: d.ExaminedArtifactID.String(),
		})
	}
	return out
}

// GateDecisionSummary projects a decided gate onto its phase. The approver's
// id and address are separate fields: a run is readable with view, so the
// address reaches every member of the project and that has to be visible in
// the contract rather than hidden inside one ambiguous "acted_by".
//
// Every field is required. The struct is either absent or complete: its one
// constructor (gateDecisionSummary below) returns nil for a gate nobody has
// decided and otherwise sets all seven from a runs.GateDecision, and every one
// of them is an always-marshaled Go scalar. Without the tag openapi-typescript
// renders each property optional, and the client's 409 surface — which reads
// decision, acted_by_email and note to say who got to the gate first — would
// have to branch on absences the server cannot produce. The property that
// CARRIES this struct on PhaseResponse stays optional for the opposite reason:
// it is a nullable $ref, and this generator gives a $ref no null branch.
type GateDecisionSummary struct {
	Decision           string    `json:"decision" required:"true"`
	Note               string    `json:"note" required:"true"`
	ActedByID          string    `json:"acted_by_id" required:"true"`
	ActedByEmail       string    `json:"acted_by_email" required:"true"`
	ActedAt            time.Time `json:"acted_at" required:"true"`
	SelfApproval       bool      `json:"self_approval" required:"true"`
	ExaminedArtifactID string    `json:"examined_artifact_id" required:"true"`
}

// PublicationResponse projects one publish intent. absent_since is
// deliberately absent: it is the settlement sweep's own bookkeeping, it moves
// on every tick, and rendering it would invite a human to act on a probe that
// is designed to be re-run.
//
// FIVE OF THE NINE ARE REQUIRED AND FOUR ARE NOT, and the `omitempty` tags
// below are what decides which. The first five are always marshaled, so the
// run-detail publication panel reads them without branching on an absence the
// server cannot produce. The last four carry omitempty and are GENUINELY
// conditional — a publication that has not pushed yet has no head SHA, one
// that has not opened a PR has neither number nor URL, and one that did not
// halt has no halt_reason — so the key really is missing on the wire for those
// four and optional is the honest rendering rather than an oversight.
type PublicationResponse struct {
	NodeID       string `json:"node_id" required:"true"`
	Status       string `json:"status" required:"true"` // pending | pushed | published | halted
	Branch       string `json:"branch" required:"true"`
	TargetBranch string `json:"target_branch" required:"true"`
	BaseSHA      string `json:"base_sha" required:"true"`
	HeadSHA      string `json:"head_sha,omitempty"`
	PRNumber     int64  `json:"pr_number,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	HaltReason   string `json:"halt_reason,omitempty"`
}

// RunEventsResponse is the JSON half of GET /runs/{id}/events — the
// poll-and-resume path, which is what the run-detail event log reads.
//
// Events is required AND nullable, and the two are independent: a Go nil slice
// marshals to null, so the type stays ["array","null"] while the key is one the
// client never has to test for. api.go:721 happens to build a non-nil
// `make([]RunEventResponse, 0, len(events))` today, which is exactly why the
// null shape is the one no test would otherwise exercise.
type RunEventsResponse struct {
	Events []RunEventResponse `json:"events" required:"true"`
}

// RunEventResponse is one row of a run's event log.
//
// All four are required because all four are always marshaled — none carries
// omitempty and none is a $ref, so `required` here is the plain claim that the
// key is present. Payload keeps its null branch for the same reason Events
// does: a nil map marshals to null, and an event with no payload is ordinary.
//
// THERE IS NO node_id COLUMN, deliberately and structurally: the node lives
// inside the untyped payload (events.go:67), which is why the run-detail log is
// flat and chronological rather than grouped under its phase. Grouping would
// depend on a field the contract cannot police.
type RunEventResponse struct {
	ID         int64          `json:"id" required:"true"`
	Kind       string         `json:"kind" required:"true"`
	Payload    map[string]any `json:"payload" required:"true"`
	OccurredAt time.Time      `json:"occurred_at" required:"true"`
}

func taskResponse(t runs.Task) TaskResponse {
	return TaskResponse{
		ID: t.ID.String(), ProjectID: t.ProjectID.String(), RepoID: t.RepoID.String(),
		WorkflowVersionID: t.WorkflowVersionID.String(), Title: t.Title, Inputs: t.Inputs,
		RequesterID: t.RequesterID.String(), Status: string(t.LatestRunStatus),
		LatestRunID: t.LatestRunID.String(), CreatedAt: t.CreatedAt,
	}
}

// gateViewer is the CALLER's half of a gate's decidability: whether they hold
// approve on the project, and who they are. The service knows the run and this
// layer knows who is asking, so the same gate projects differently for two
// principals on purpose.
//
// Its zero value is a caller who may decide nothing, which is what the two
// shallow projections below pass — they carry no phases at all, so no gate of
// theirs is ever projected.
type gateViewer struct {
	canApprove bool
	actorID    uuid.UUID
}

// noGateViewer is the viewer for a projection that has no gate to project. The
// zero value already fails closed, but only by accident of which field comes
// first; naming it says so at the call site and survives a reordering.
func noGateViewer() gateViewer { return gateViewer{canApprove: false} }

func gateEvidenceResponse(ev runs.GateEvidence, v gateViewer) GateEvidenceResponse {
	haveExamined := ev.ExaminedArtifactID != uuid.Nil
	canDecide, reason := decidability(v.canApprove, v.actorID, ev.RequesterID, ev.AllowSelfApproval,
		haveExamined, ev.WallDeadline, ev.AsOf)
	out := GateEvidenceResponse{
		ExaminedArtifactType: ev.ExaminedArtifactType,
		CanDecide:            canDecide,
		CannotDecideReason:   reason,
		WallDeadline:         ev.WallDeadline,
	}
	// A zero id is "the run never produced it", and the empty string is how
	// that reaches a client — the all-zeroes uuid would read as an artifact
	// they could go and fetch.
	if haveExamined {
		out.ExaminedArtifactID = ev.ExaminedArtifactID.String()
	}
	if ev.SummaryArtifactID != nil {
		id := ev.SummaryArtifactID.String()
		out.SummaryArtifactID = &id
	}
	return out
}

func runResponse(d runs.RunDetail, v gateViewer) RunResponse {
	r := d.Run
	out := RunResponse{
		ID: r.ID.String(), TaskID: r.TaskID.String(), Status: string(r.Status),
		TaskTitle: d.Labels.TaskTitle, RepoSlug: d.Labels.RepoSlug,
		WorkflowName:   d.Labels.WorkflowName,
		DefinitionHash: r.DefinitionHash, BaseSHA: r.BaseSHA, PolicySnapshot: r.Snapshot,
		Generation:      r.Generation,
		ExecutionUsedMS: r.ExecutionUsedMS, WallDeadline: r.WallDeadline,
		Outcome: outcomeResponse(r.Outcome), CreatedAt: r.CreatedAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Phases:       make([]PhaseResponse, 0, len(d.Phases)),
		Artifacts:    make([]ArtifactResponse, 0, len(d.Artifacts)),
		Publications: make([]PublicationResponse, 0, len(d.Publications)),
	}
	for _, p := range d.Phases {
		phase := PhaseResponse{
			NodeID: p.NodeID, Attempt: p.Attempt, MaxAttempts: p.MaxAttempts, Status: string(p.Status),
			Generation: p.Generation, Outcome: outcomeResponse(p.Outcome),
			CreatedAt: p.CreatedAt, FinishedAt: p.FinishedAt, Decision: gateDecisionSummary(p.Decision),
		}
		if p.Evidence != nil {
			ev := gateEvidenceResponse(*p.Evidence, v)
			phase.Evidence = &ev
		}
		out.Phases = append(out.Phases, phase)
	}
	for _, a := range d.Artifacts {
		out.Artifacts = append(out.Artifacts, ArtifactResponse{
			ID: a.ID.String(), NodeID: a.NodeID, Attempt: a.Attempt, Generation: a.Generation,
			Type: a.Type, ParentIDs: parentIDStrings(a.ParentIDs), CreatedAt: a.CreatedAt,
		})
	}
	for _, p := range d.Publications {
		out.Publications = append(out.Publications, PublicationResponse{
			NodeID: p.NodeID, Status: p.Status, Branch: p.Branch, TargetBranch: p.TargetBranch,
			BaseSHA: p.BaseSHA, HeadSHA: p.HeadSHA, PRNumber: p.PRNumber, PRURL: p.PRURL,
			HaltReason: p.HaltReason,
		})
	}
	return out
}

// parentIDStrings projects an artifact's lineage for the wire. It builds with
// make so a lineage-less artifact ships [] and NEVER null — an invariant both
// artifact projections depend on and ArtifactPayloadResponse's doc comment
// asserts, which is why one function holds it rather than two copies.
func parentIDStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

// artifactPayloadResponse projects one artifact's content. The run id is passed
// in rather than read off the artifact: the service has already required the two
// to agree, so this echoes the run the CALLER addressed and there is no second
// value that could disagree with it.
func artifactPayloadResponse(runID uuid.UUID, a runs.ArtifactWithPayload) ArtifactPayloadResponse {
	return ArtifactPayloadResponse{
		ID: a.ID.String(), RunID: runID.String(), NodeID: a.NodeID,
		Attempt: a.Attempt, Generation: a.Generation, Type: a.Type,
		ParentIDs: parentIDStrings(a.ParentIDs), CreatedAt: a.CreatedAt, Payload: a.Payload,
	}
}

// runListPageResponse projects one page. next is the service's empty string for
// "there is no next page", and it becomes a JSON null rather than an absent key
// or an empty string: the client branches on the value, and "" is a cursor
// shape it would have to know is special.
func runListPageResponse(list []runs.RunSummary, next string) RunListPageResponse {
	out := RunListPageResponse{Runs: make([]RunSummaryResponse, 0, len(list))}
	if next != "" {
		out.NextCursor = &next
	}
	for _, r := range list {
		row := RunSummaryResponse{
			ID: r.ID.String(), TaskID: r.TaskID.String(), TaskTitle: r.TaskTitle,
			ProjectID: r.ProjectID.String(), ProjectName: r.ProjectName,
			RepoSlug: r.RepoSlug, WorkflowName: r.WorkflowName,
			Status: string(r.Status), Attempt: r.Attempt, Generation: r.Generation,
			RequesterID: r.RequesterID.String(), CreatedAt: r.CreatedAt,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			PhaseSummary: make([]PhaseSummaryResponse, 0, len(r.Phases)),
		}
		for _, p := range r.Phases {
			entry := PhaseSummaryResponse{
				NodeID: p.NodeID, NodeType: p.NodeType,
				Attempt: p.Attempt, Generation: p.Generation,
			}
			if p.Status != nil {
				status := string(*p.Status)
				entry.Status = &status
			}
			row.PhaseSummary = append(row.PhaseSummary, entry)
		}
		out.Runs = append(out.Runs, row)
	}
	return out
}

func gateDecisionSummary(d *runs.GateDecision) *GateDecisionSummary {
	if d == nil {
		return nil
	}
	return &GateDecisionSummary{
		Decision: d.Decision, Note: d.Note, ActedByID: d.ActedByID.String(),
		ActedByEmail: d.ActedByEmail, ActedAt: d.ActedAt, SelfApproval: d.SelfApproval,
		ExaminedArtifactID: d.ExaminedArtifactID.String(),
	}
}

func approvalResponse(g runs.WaitingGate, v gateViewer) ApprovalResponse {
	return ApprovalResponse{
		RunID: g.RunID.String(), TaskID: g.TaskID.String(), ProjectID: g.ProjectID.String(),
		TaskTitle: g.TaskTitle, NodeID: g.NodeID, Attempt: g.Attempt, Generation: g.Generation,
		RequesterID: g.RequesterID.String(), WaitingSince: g.WaitingSince,
		Evidence: gateEvidenceResponse(g.Evidence, v),
	}
}

func outcomeResponse(o *runs.Outcome) *OutcomeResponse {
	if o == nil {
		return nil
	}
	return &OutcomeResponse{Class: string(o.Class), Code: o.Code, Message: o.Message, RetryAt: o.RetryAt}
}

func eventResponse(e runs.RunEventRecord) RunEventResponse {
	return RunEventResponse{ID: e.ID, Kind: e.Kind, Payload: e.Payload, OccurredAt: e.OccurredAt}
}
