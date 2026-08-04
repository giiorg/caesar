package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

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

type API struct {
	svc    *runs.Service
	auth   authn.SessionAuthenticator
	authz  authn.Authorizer
	grants GrantSource
}

func New(svc *runs.Service, auth authn.SessionAuthenticator, authz authn.Authorizer, grants GrantSource) *API {
	return &API{svc: svc, auth: auth, authz: authz, grants: grants}
}

// Operations is the spine's route table: mux registration (httpx.Mount)
// and OpenAPI generation (internal/contracts) both consume it. Nothing
// here is admin-only — every read needs view, launching needs run.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/tasks", ID: "createTask", Summary: "Create a task and its first run (run grant)",
			Request: CreateTaskRequest{}, Response: TaskResponse{}, Status: http.StatusCreated,
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
			Handler: a.authenticated(a.handleRerunTask),
		},
		{
			Pattern: "GET /api/v1/tasks/{id}/runs", ID: "listTaskRuns", Summary: "List a task's runs, newest first (view)",
			Response: RunListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListTaskRuns),
		},
		{
			Pattern: "GET /api/v1/runs/{id}", ID: "getRun", Summary: "Get a run with phases and artifact metadata (view)",
			Response: RunResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetRun),
		},
		{
			Pattern: "GET /api/v1/runs/{id}/events", ID: "listRunEvents", Summary: "Poll a run's events after an id (view)",
			Response: RunEventsResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListRunEvents),
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
// task belongs to — runs carry no project of their own.
func (a *API) runScoped(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, min authn.GrantLevel) (runs.Run, []runs.Phase, []runs.ArtifactMeta, bool) {
	id, err := pathUUID(r, "no such run")
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Run{}, nil, nil, false
	}
	run, phases, arts, err := a.svc.GetRun(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Run{}, nil, nil, false
	}
	task, err := a.svc.GetTask(r.Context(), run.TaskID)
	if err != nil {
		httpx.Problem(w, r, err)
		return runs.Run{}, nil, nil, false
	}
	if !a.authorize(w, r, sess, task.ProjectID, min) {
		return runs.Run{}, nil, nil, false
	}
	return run, phases, arts, true
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
	httpx.JSON(w, http.StatusCreated, runResponse(run, nil, nil))
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
	out := RunListResponse{Runs: make([]RunResponse, 0, len(list))}
	for _, run := range list {
		out.Runs = append(out.Runs, runResponse(run, nil, nil))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetRun(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	run, phases, arts, ok := a.runScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, runResponse(run, phases, arts))
}

func (a *API) handleListRunEvents(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	run, _, _, ok := a.runScoped(w, r, sess, authn.GrantView)
	if !ok {
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
