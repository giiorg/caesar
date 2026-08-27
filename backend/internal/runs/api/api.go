package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// GrantSource lists a member's grants for project-visibility filtering.
// identity.Service satisfies it; app wiring passes it in.
type GrantSource interface {
	GrantsForUser(ctx context.Context, userID uuid.UUID) ([]authn.Grant, error)
}

// StreamServer is runs/api's outbound seam to the realtime context.
// runs must not import realtime (bounded contexts are peers), so app
// wires realtime's splice in behind this interface — which is why nothing in
// the signature names a realtime type.
//
// ServeRunStream blocks until the client goes away or the stream ends, and it
// OWNS the response from the moment it writes: the first SSE frame commits the
// 200 forever, and no status is available after that.
//
// So its error means exactly one thing: the stream failed BEFORE it committed
// anything, and the caller may still answer with a real status. Every later
// ending — a closed tab, a slow consumer, shutdown, a revocation acted on, a
// write that failed — returns nil and is reported by the implementation, which
// is the only side that can tell a routine ending from a fault (that reading
// is realtime's close reasons, which runs must never name).
// projectID is the run's project, which this side has already resolved and
// authorized against. It travels with the run id because the implementation
// needs it to keep enforcing the grant for the LIFETIME of the stream, and a
// run carries no project of its own — so the alternative is the far side
// re-reading the run and its task to learn what this side already knows.
type StreamServer interface {
	ServeRunStream(ctx context.Context, w http.ResponseWriter, runID, projectID, userID uuid.UUID, lastEventID int64, hasLastID bool) error
}

type API struct {
	svc    *runs.Service
	auth   authn.SessionAuthenticator
	authz  authn.Authorizer
	grants GrantSource
	// streams is nil wherever nothing wired it: internal/contracts builds this
	// table with every dependency nil for metadata-only generation and never
	// runs a handler. internal/app passes the live realtime surface. What a nil
	// one costs at request time is streamRunEvents' first check.
	streams StreamServer
	// log records the one thing this layer swallows rather than reports: a
	// label lookup that failed AFTER a mutation already committed (see
	// handleRerunTask). Everything else here becomes a problem+json response,
	// so this is the only writer.
	log *slog.Logger
}

func New(svc *runs.Service, auth authn.SessionAuthenticator, authz authn.Authorizer, grants GrantSource, streams StreamServer, log *slog.Logger) *API {
	// A nil logger is legal for the same reason a nil StreamServer is:
	// internal/contracts builds this table with every dependency nil to
	// generate metadata and never runs a handler. It defaults rather than
	// stays nil so a future handler cannot nil-panic on it.
	if log == nil {
		log = slog.Default()
	}
	return &API{svc: svc, auth: auth, authz: authz, grants: grants, streams: streams, log: log}
}

// Operations is the spine's route table: mux registration (httpx.Mount)
// and OpenAPI generation (internal/contracts) both consume it. Nothing
// here is admin-only — every read needs view, launching needs run.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/tasks", ID: "createTask", Summary: "Create a task and its first run (run grant)",
			Request: CreateTaskRequest{}, Response: TaskResponse{}, Status: http.StatusCreated,
			// Intake resolves the repository's head on the forge before it
			// writes anything, so the forge's answer is part of this endpoint's
			// answer set: 404 when the repository or its default branch is not
			// there, 429 when the forge is throttling or unreachable. A client
			// that cannot see those reads both as a bug in caesar.
			Errors:  []int{http.StatusNotFound, http.StatusTooManyRequests},
			Handler: a.authenticated(a.handleCreateTask),
		},
		{
			Pattern: "GET /api/v1/tasks", ID: "listTasks", Summary: "List tasks visible to the caller",
			Response: TaskListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListTasks),
		},
		{
			Pattern: "GET /api/v1/tasks/{id}", ID: "getTask", Summary: "Get a task (view)",
			Response: TaskResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetTask),
		},
		{
			Pattern: "POST /api/v1/tasks/{id}/runs", ID: "rerunTask", Summary: "Re-run a task, superseding a pending run (run grant)",
			Response: RunResponse{}, Status: http.StatusCreated,
			// Same forge resolution as createTask, plus this endpoint's own 404
			// for a task that does not exist.
			Errors:  []int{http.StatusNotFound, http.StatusTooManyRequests},
			Handler: a.authenticated(a.handleRerunTask),
		},
		{
			Pattern: "GET /api/v1/tasks/{id}/runs", ID: "listTaskRuns", Summary: "List a task's runs, newest first (view)",
			Response: RunListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListTaskRuns),
		},
		{
			Pattern: "GET /api/v1/runs", ID: "listRuns",
			Summary: "List runs on the projects the caller holds any grant on, newest first. " +
				"Cursor-paginated: pass `next_cursor` back as `cursor`; a null `next_cursor` is the last page.",
			Request: ListRunsQuery{}, Response: RunListPageResponse{}, Status: http.StatusOK,
			// 401 and no 403, exactly as listApprovals: authenticated() refuses
			// a missing cookie before the handler runs, and the handler itself
			// scopes rather than refuses — an ungranted member gets 200 and an
			// empty page. Seeing nothing and being refused are different
			// answers and this endpoint only ever gives the first.
			Errors:  []int{http.StatusUnauthorized},
			Handler: a.authenticated(a.handleListRuns),
		},
		{
			Pattern: "GET /api/v1/runs/{id}", ID: "getRun", Summary: "Get a run with phases and artifact metadata (view)",
			Response: RunResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetRun),
		},
		{
			Pattern: "GET /api/v1/runs/{id}/artifacts/{artifact_id}", ID: "getArtifact",
			Summary: "Read one artifact of a run, including its payload (view). " +
				"A `patch` payload carries `diff` as base64 by contract — a patch is " +
				"arbitrary bytes and JSON string encoding would replace invalid UTF-8 " +
				"silently. Decode once for display; never round-trip it.",
			Response: ArtifactPayloadResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetArtifact),
		},
		{
			Pattern: "GET /api/v1/runs/{id}/events", ID: "listRunEvents",
			// One URL, two representations. The stream's frame schema is the
			// SECOND response media type on this operation, declared in
			// internal/contracts because it is a realtime type and runs may not
			// name one; the poll's envelope below stays the primary.
			Summary: "Poll a run's events after an id (view). " +
				"With `Accept: text/event-stream` the same URL answers a server-sent stream " +
				"instead — snapshot-then-deltas, resumed with Last-Event-ID — and every other " +
				"Accept keeps this JSON poll. " +
				"A stream whose principal loses access to the run ends with a terminal frame " +
				"that carries NO id — `event: forbidden` with `data: {\"error\":\"forbidden\"}` " +
				"— and then closes: the 200 is committed with the first byte, so a revocation " +
				"can no longer be a 403.",
			Response: RunEventsResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListRunEvents),
		},
		{
			Pattern: "POST /api/v1/runs/{id}/gates/{node}/decision", ID: "decideGate",
			Summary: "Approve, request changes on, or reject a waiting gate (approve grant). " +
				"The generation in the response is the one decided on approve and reject, " +
				"and the new one the revision opened on request_changes.",
			Request: GateDecisionRequest{}, Response: GateDecisionResponse{}, Status: http.StatusOK,
			// 409 is this endpoint's ordinary non-success answer — the gate was
			// already decided, the run moved, the revision rounds are spent — and
			// a client that cannot see it in the contract reads it as a bug.
			Errors:  []int{http.StatusConflict},
			Handler: a.authenticated(a.handleDecideGate),
		},
		{
			Pattern: "GET /api/v1/approvals", ID: "listApprovals",
			// No grant level in the parenthetical, unlike every sibling above,
			// because this endpoint enforces none: it scopes to the projects
			// the caller holds ANY grant on. Seeing a gate is not being
			// allowed to decide it — decideGate is where approve is enforced.
			Summary:  "List gates awaiting a decision on the projects the caller holds any grant on (an admin sees every one). Unbounded by design at V1.",
			Response: ApprovalListResponse{}, Status: http.StatusOK,
			// 401 and no 403: authenticated() refuses a missing or expired
			// cookie before the handler runs, but the handler itself never
			// refuses an authenticated caller — it scopes to the projects they
			// hold any grant on and answers 200 with an empty list when that
			// set is empty. Seeing nothing and being refused are different
			// answers and this endpoint only ever gives the first.
			Errors:  []int{http.StatusUnauthorized},
			Handler: a.authenticated(a.handleListApprovals),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession)

func (a *API) authenticated(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(authn.SessionCookie)
		if err != nil {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassUnauthenticated, "authentication required"))
			return
		}
		sess, err := a.auth.AuthenticateSession(r.Context(), c.Value)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		next(w, r, sess)
	})
}

// authorize writes a 403 problem and returns false unless sess holds min
// on projectID (RBAC matrix in the plan header).
func (a *API) authorize(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, projectID uuid.UUID, min authn.GrantLevel) bool {
	ok, err := a.authz.Authorize(r.Context(), sess.Principal, projectID, min)
	if err != nil {
		httpx.Problem(w, r, err)
		return false
	}
	if !ok {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "insufficient project grant"))
		return false
	}
	return true
}

// decode mirrors the peer API packages' strict decoder (contexts never
// share hand-rolled helpers across the boundary; ten lines beats a new
// seam).
func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, xerrors.Wrap(xerrors.ClassInvalid, "invalid request body", err)
	}
	return v, nil
}

func pathUUID(r *http.Request, notFound string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassNotFound, notFound)
	}
	return id, nil
}

func (a *API) handleCreateTask(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateTaskRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	repoID, err := uuid.Parse(req.RepoID)
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such repo"))
		return
	}
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such workflow"))
		return
	}
	projectID, err := a.svc.RepoProject(r.Context(), repoID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, projectID, authn.GrantRun) {
		return
	}
	task, _, err := a.svc.CreateTask(r.Context(), sess.Principal, runs.NewTask{
		RepoID: repoID, WorkflowID: workflowID, WorkflowVersion: req.WorkflowVersion,
		Title: req.Title, Inputs: req.Inputs,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, taskResponse(task))
}

func (a *API) handleListTasks(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	var (
		list []runs.Task
		err  error
	)
	if sess.Role == authn.RoleAdmin {
		list, err = a.svc.ListTasks(r.Context())
	} else {
		var grants []authn.Grant
		grants, err = a.grants.GrantsForUser(r.Context(), sess.UserID)
		if err == nil {
			ids := make([]uuid.UUID, 0, len(grants))
			for _, g := range grants {
				ids = append(ids, g.ProjectID)
			}
			list, err = a.svc.ListTasksByProjects(r.Context(), ids)
		}
	}
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := TaskListResponse{Tasks: make([]TaskResponse, 0, len(list))}
	for _, t := range list {
		out.Tasks = append(out.Tasks, taskResponse(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// taskScoped resolves {id} to a task and enforces min on its project.
// The fetch precedes the check, so an unknown id is 404 for everyone —
// the peer contexts' semantics.
func (a *API) taskScoped(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, min authn.GrantLevel) (runs.Task, bool) {
	id, err := pathUUID(r, "no such task")
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Task{}, false
	}
	task, err := a.svc.GetTask(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Task{}, false
	}
	if !a.authorize(w, r, sess, task.ProjectID, min) {
		return runs.Task{}, false
	}
	return task, true
}

// runScoped resolves {id} to a run and enforces min on the project its
// task belongs to — runs carry no project of their own. The fetch precedes
// the check, so an unknown id is 404 for everyone.
//
// It reads the run row only. The projection its one page-rendering caller
// needs is five queries, and the event poll runs this on every tick.
//
// The TASK it authorized against is RETURNED rather than discarded. It cost a
// query to learn, and neither the project it carries nor the three labels it
// pins are recoverable from the run — so the callers that need either would
// otherwise repeat both reads to obtain a value this function already had in
// hand.
func (a *API) runScoped(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, min authn.GrantLevel) (runs.Run, runs.Task, bool) {
	id, err := pathUUID(r, "no such run")
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Run{}, runs.Task{}, false
	}
	run, err := a.svc.GetRunSummary(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Run{}, runs.Task{}, false
	}
	task, err := a.svc.GetTask(r.Context(), run.TaskID)
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Run{}, runs.Task{}, false
	}
	if !a.authorize(w, r, sess, task.ProjectID, min) {
		return runs.Run{}, runs.Task{}, false
	}
	return run, task, true
}

func (a *API) handleGetTask(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	task, ok := a.taskScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, taskResponse(task))
}

func (a *API) handleRerunTask(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	task, ok := a.taskScoped(w, r, sess, authn.GrantRun)
	if !ok {
		return
	}
	run, err := a.svc.Rerun(r.Context(), sess.Principal, task.ID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	// A freshly minted run carries no phases, so no gate of it is projected and
	// the empty viewer is the honest answer rather than a shortcut. The LABELS
	// are still resolved: they belong to the task rather than to the run, and
	// leaving them blank would ship a projection whose header renders nothing
	// on the very screen a re-run lands you on.
	//
	// THIS PATH DEGRADES WHERE THE READ PATHS REPORT, and the asymmetry is the
	// point. Rerun has already COMMITTED a run by the time labels are looked
	// up; answering 500 would hand the client a failure for a mutation that
	// succeeded, and the obvious client response — retry — walks straight into
	// the one-live-run-per-task conflict and looks like a different bug. A
	// header that renders three blank strings is recoverable by a refresh; a
	// duplicate rerun attempt is not. GET /runs/{id} has nothing committed to
	// protect, so it reports the fault instead.
	labels, err := a.svc.LabelsForTask(r.Context(), task)
	if err != nil {
		a.log.WarnContext(r.Context(), "rerun labels unresolved; responding without them",
			slog.String("run_id", run.ID.String()),
			slog.String("task_id", task.ID.String()),
			slog.String("correlation_id", httpx.CorrelationID(r.Context())),
			slog.String("error", err.Error()))
		labels = runs.RunLabels{}
	}
	httpx.JSON(w, http.StatusCreated, runResponse(runs.RunDetail{Run: run, Labels: labels}, noGateViewer()))
}

func (a *API) handleListTaskRuns(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	task, ok := a.taskScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	list, err := a.svc.ListRunsForTask(r.Context(), task.ID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	// Every run of one task shares that task's labels, so this is ONE
	// resolution for the whole list rather than one per row.
	labels, err := a.svc.LabelsForTask(r.Context(), task)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := RunListResponse{Runs: make([]RunResponse, 0, len(list))}
	for _, run := range list {
		// List entries stay shallow — no phases, so no gate evidence.
		out.Runs = append(out.Runs, runResponse(runs.RunDetail{Run: run, Labels: labels}, noGateViewer()))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetRun(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	run, task, ok := a.runScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	// The run is READABLE with view and DECIDABLE with approve, so a second
	// question is asked of the same project: may this caller decide a gate they
	// can see? Authorize answers it for an admin too, whose grants are empty.
	//
	// It is asked on EVERY run, including the ones with no waiting gate, and
	// deliberately. Skipping it would mean deferring the authorization question
	// until after GetRunDetail so the phases could be scanned first — putting an
	// access check downstream of the data read, which is the ordering every
	// other handler here avoids — to save one indexed single-row lookup
	// (identity/service.go: GetGrantLevel, and free for an admin) on a page that
	// has already run several queries.
	canApprove, err := a.authz.Authorize(r.Context(), sess.Principal, task.ProjectID, authn.GrantApprove)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	detail, err := a.svc.GetRunDetail(r.Context(), run.ID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	// The header's three labels, resolved off the task runScoped already read.
	// GetRunDetail deliberately does not do this: a label is a presentation
	// concern with a port of its own, and folding it into the domain read would
	// make every service-level reader of a run wire a label source.
	detail.Labels, err = a.svc.LabelsForTask(r.Context(), task)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, runResponse(detail, gateViewer{canApprove: canApprove, actorID: sess.UserID}))
}

// handleGetArtifact serves one artifact's content. Authorization is on the RUN
// (spec §2.2): an artifact id may not be used to reach across a project
// boundary, so the run is resolved and authorized first and the artifact is
// then required to belong to it.
func (a *API) handleGetArtifact(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	run, _, ok := a.runScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	// A path segment that is not a uuid names no artifact, so it is this
	// endpoint's 404 and not a 400: reporting the parse failure would turn a
	// caller's typo into an unclassified error, which httpx.Problem answers 500.
	artifactID, err := uuid.Parse(r.PathValue("artifact_id"))
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such artifact"))
		return
	}
	art, err := a.svc.ArtifactPayload(r.Context(), run.ID, artifactID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, artifactPayloadResponse(run.ID, art))
}

// decidability answers §5 for one gate. It is SERVER-SIDE because neither half
// is visible from the list: policy_snapshot is on the run (so a client would
// fetch each one — the N+1 this endpoint exists to prevent) and the approve
// grant is not in the response at all.
//
// ORDER MATTERS AND IS DELIBERATE, and it is decideGate's own order. That
// endpoint refuses on the grant first (handleDecideGate authorizes
// GrantApprove before DecideGate opens its transaction), then on the wall
// deadline (gates.go:176), then on the policy (:189), and only then resolves
// the examined artifact (:193) — so checking in the same sequence makes
// cannot_decide_reason name the refusal the caller would ACTUALLY get rather
// than some other true thing about the gate.
//
// THE GRANT COMES FIRST BECAUSE THE ENDPOINT PUTS IT THERE, and for no other
// reason. A caller short of approve on an expired run is told "not_approver"
// not because raising a grant would help them — on a run that is finished for
// everybody it would not — but because 403 is the answer they would receive:
// the authorize call never reaches gates.go:176 at all. This field's whole
// contract is to name the refusal that would actually arrive, so an ordering
// argued from what a reader can DO would have to override that contract to
// win, and it does not. Pinned by "not_approver still wins" in
// gateevidence_integration_test.go.
//
// THE DEADLINE IS COMPARED DB-CLOCK TO DB-CLOCK. `now` is q.DBNow from the
// same read that carried `wallDeadline`, never s.clock.Now(): both sides of a
// deadline comparison have to be database timestamps or app-clock skew moves
// the boundary, and this projection would then disagree with the transaction
// that actually refuses (DBNow's contract, store/queries.sql:114-119).
//
// haveExamined is last because it is not the caller's problem until the caller
// is otherwise able. A gate whose examined artifact the run never produced is
// undecidable BY ANYONE — decideGate answers 500, not a refusal — so the
// surface has to say so rather than ship a control that provokes it.
func decidability(canApprove bool, actorID, requesterID uuid.UUID, allowSelf, haveExamined bool,
	wallDeadline, now time.Time,
) (bool, string) {
	if !canApprove {
		return false, "not_approver"
	}
	if !wallDeadline.After(now) {
		return false, "deadline_expired"
	}
	if actorID == requesterID && !allowSelf {
		return false, "self_approval_forbidden"
	}
	if !haveExamined {
		return false, "evidence_missing"
	}
	return true, ""
}

// handleDecideGate settles one gate. The project comes from the run, so the
// grant check precedes any write; GateProject answers 404 for a run that does
// not exist, which keeps an unknown id a 404 for everybody rather than an
// existence oracle. A run that DOES exist outside the caller's grants is a 403,
// the same as every other run-scoped route.
func (a *API) handleDecideGate(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[GateDecisionRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	runID, err := pathUUID(r, "no such run")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	// Unreachable through the mux, which redirects an empty wildcard segment
	// rather than matching it — kept because a route table is edited by hand
	// and this is the cheap half of being wrong about that. No test covers it.
	node := r.PathValue("node")
	if node == "" {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such gate"))
		return
	}
	projectID, err := a.svc.GateProject(r.Context(), runID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, projectID, authn.GrantApprove) {
		return
	}
	res, err := a.svc.DecideGate(r.Context(), sess.Principal, runs.GateDecisionInput{
		RunID: runID, NodeID: node, Decision: runs.Decision(req.Decision), Note: req.Note,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := GateDecisionResponse{
		DecisionID: res.DecisionID.String(), Decision: req.Decision,
		RunStatus: string(res.RunStatus), Generation: res.Generation,
	}
	if res.AuthorizationID != nil {
		out.AuthorizationID = res.AuthorizationID.String()
	}
	httpx.JSON(w, http.StatusOK, out)
}

// handleListRuns is the run list. It scopes exactly as the approvals inbox
// does — the projects the caller holds ANY grant on, every project for an admin
// — and it is PAGINATED from the outset because it is new: GET /tasks and
// GET /approvals already have the unbounded cliff and there is no reason to
// build a third.
func (a *API) handleListRuns(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	filter, err := runListFilter(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if sess.Role == authn.RoleAdmin {
		filter.All = true
	} else {
		grants, err := a.grants.GrantsForUser(r.Context(), sess.UserID)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		// Built even when it is empty, and passed as an empty set rather than
		// as "no filter": an ungranted member sees NOTHING, and inferring the
		// admin view from an empty list is how they would see everything.
		ids := make([]uuid.UUID, 0, len(grants))
		for _, g := range grants {
			ids = append(ids, g.ProjectID)
		}
		filter.ProjectIDs = ids
	}
	list, next, err := a.svc.ListRuns(r.Context(), filter)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, runListPageResponse(list, next))
}

// runListFilter parses the query string. Every malformed value is a FIELD ERROR
// naming the parameter: an unparseable task_id is neither a 404 (the caller
// named no run) nor a silent ignore (which would answer with the whole list and
// look like the filter did nothing), and a cursor this endpoint did not issue
// is the same kind of mistake rather than a silent page one.
func runListFilter(r *http.Request) (runs.RunListFilter, error) {
	q := r.URL.Query()
	f := runs.RunListFilter{Status: q.Get("status"), Cursor: q.Get("cursor")}
	if raw := q.Get("task_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return runs.RunListFilter{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
				WithFields(xerrors.FieldError{Field: "task_id", Message: "must be a uuid"})
		}
		f.TaskID = &id
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			return runs.RunListFilter{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
				WithFields(xerrors.FieldError{Field: "limit", Message: "must be a positive integer"})
		}
		// An oversized limit is CLAMPED rather than refused: the cap is this
		// server's protection, not a contract the caller agreed to, and a page
		// of 200 is a complete answer to "give me 1000".
		f.Limit = int32(n)
	}
	return f, nil
}

// handleListApprovals is the approvals inbox. It is unbounded by design at V1:
// the waiting set is the size of a human's attention, not of the run history.
func (a *API) handleListApprovals(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	var (
		list []runs.WaitingGate
		err  error
		// levels is the caller's grant per project, built from the SAME
		// GrantsForUser call the scoping below already makes — can_decide costs
		// this handler no new query. It stays empty for an admin, whose
		// authority comes from the role instead.
		levels = map[uuid.UUID]authn.GrantLevel{}
	)
	isAdmin := sess.Role == authn.RoleAdmin
	if isAdmin {
		list, err = a.svc.ListWaitingGates(r.Context(), nil, true)
	} else {
		var grants []authn.Grant
		grants, err = a.grants.GrantsForUser(r.Context(), sess.UserID)
		if err == nil {
			// The inbox shows what the caller can SEE; the decision endpoint
			// is what enforces approve. A view-only member seeing that a gate
			// is pending is information they already have from the run, and
			// every other list endpoint here scopes the same way — which is
			// why each row then says whether they may decide it.
			ids := make([]uuid.UUID, 0, len(grants))
			for _, g := range grants {
				ids = append(ids, g.ProjectID)
				levels[g.ProjectID] = g.Level
			}
			list, err = a.svc.ListWaitingGates(r.Context(), ids, false)
		}
	}
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := ApprovalListResponse{Approvals: make([]ApprovalResponse, 0, len(list))}
	for _, g := range list {
		// An admin passes Authorize on every project regardless of grants, so
		// deciding this from the map alone would report not_approver on every
		// row of the one inbox that sees every row.
		canApprove := isAdmin || levels[g.ProjectID].AtLeast(authn.GrantApprove)
		out.Approvals = append(out.Approvals, approvalResponse(g, gateViewer{
			canApprove: canApprove, actorID: sess.UserID,
		}))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// handleListRunEvents answers this run's events either way the client asked
// for them. The grant check comes FIRST and is shared: every answer the poll
// gives a caller who may not see this run — 404 for an unknown id, 403 for one
// outside their grants — the stream gives too, and neither branch can drift
// from the other because there is only one check.
func (a *API) handleListRunEvents(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	run, task, ok := a.runScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	// Values, not Get: a client may legally split its Accept ranges across
	// repeated headers, and Get would see only the first — which for a client
	// that put its refusal in the second would be a stream it declined.
	if acceptsEventStream(strings.Join(r.Header.Values("Accept"), ",")) {
		a.streamRunEvents(w, r, sess, run.ID, task.ProjectID)
		return
	}
	after, err := eventCursor(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	// A zero limit takes the service's page cap.
	events, err := a.svc.ListRunEvents(r.Context(), run.ID, after, 0)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := RunEventsResponse{Events: make([]RunEventResponse, 0, len(events))}
	for _, e := range events {
		out.Events = append(out.Events, eventResponse(e))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// streamRunEvents hands the request to the realtime context.
//
// It writes nothing itself. Everything it can still answer with a status
// happens before the port is called, because the port's first frame commits
// the 200 forever; after that the only thing left to report a returned error
// to is the log, and the port has already done that where the close reasons
// are in scope (see StreamServer).
func (a *API) streamRunEvents(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, runID, projectID uuid.UUID) {
	// No port means nobody wired the realtime context, and a wiring omission
	// must be LOUD. Falling back to the poll is the tempting alternative and
	// the wrong one: the endpoint would look like it works and the streaming
	// half would simply never exist, with nothing anywhere to say so.
	if a.streams == nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassInternal, "realtime streaming is not wired"))
		return
	}
	lastID, hasLastID, err := httpx.LastEventID(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if err := a.streams.ServeRunStream(r.Context(), w, runID, projectID, sess.UserID, lastID, hasLastID); err != nil {
		httpx.Problem(w, r, err)
	}
}

// eventCursor reads the poll cursor; absent means "from the beginning".
func eventCursor(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 {
		return 0, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "after", Message: "must be a non-negative integer"})
	}
	return after, nil
}
