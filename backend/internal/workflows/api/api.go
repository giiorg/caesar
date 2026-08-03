package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

type API struct {
	svc  *workflows.Service
	auth authn.SessionAuthenticator
}

func New(svc *workflows.Service, auth authn.SessionAuthenticator) *API {
	return &API{svc: svc, auth: auth}
}

// Operations is workflows' route table: mux registration (httpx.Mount)
// and OpenAPI generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/workflows", ID: "createWorkflow", Summary: "Create a workflow (admin)",
			Request: CreateWorkflowRequest{}, Response: WorkflowResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateWorkflow),
		},
		{
			Pattern: "GET /api/v1/workflows", ID: "listWorkflows", Summary: "List workflows",
			Response: WorkflowListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListWorkflows),
		},
		{
			Pattern: "GET /api/v1/workflows/{id}", ID: "getWorkflow", Summary: "Get a workflow",
			Response: WorkflowResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetWorkflow),
		},
		{
			Pattern: "PATCH /api/v1/workflows/{id}", ID: "updateWorkflow", Summary: "Update name or description (admin)",
			Request: UpdateWorkflowRequest{}, Response: WorkflowResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleUpdateWorkflow),
		},
		{
			Pattern: "DELETE /api/v1/workflows/{id}", ID: "deleteWorkflow", Summary: "Delete a workflow and its versions (admin)",
			Status:  http.StatusNoContent,
			Handler: a.adminOnly(a.handleDeleteWorkflow),
		},
		{
			// The single write door (spec §4). 201 on creation; posting
			// content that already exists returns the existing version
			// with 200 (idempotent, decision 6).
			Pattern: "POST /api/v1/workflows/{id}/versions", ID: "createWorkflowVersion", Summary: "Validate and persist an immutable definition version (admin)",
			Request: CreateWorkflowVersionRequest{}, Response: WorkflowVersionResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateWorkflowVersion),
		},
		{
			Pattern: "GET /api/v1/workflows/{id}/versions", ID: "listWorkflowVersions", Summary: "List version metadata, newest first",
			Response: WorkflowVersionListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListWorkflowVersions),
		},
		{
			Pattern: "GET /api/v1/workflows/{id}/versions/{version}", ID: "getWorkflowVersion", Summary: "Get one version with its document",
			Response: WorkflowVersionResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetWorkflowVersion),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession)

// authenticated/adminOnly mirror the other API packages (contexts never
// share hand-rolled helpers across the boundary; the copies are small).
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

func (a *API) adminOnly(next sessionHandler) http.Handler {
	return a.authenticated(func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
		if sess.Role != authn.RoleAdmin {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "admin role required"))
			return
		}
		next(w, r, sess)
	})
}

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

func pathVersion(r *http.Request) (int32, error) {
	v, err := strconv.ParseInt(r.PathValue("version"), 10, 32)
	if err != nil || v < 1 {
		return 0, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return int32(v), nil
}

func (a *API) handleCreateWorkflow(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateWorkflowRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	wf, err := a.svc.CreateWorkflow(r.Context(), sess.Principal, req.Name, req.Description)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, workflowResponse(wf))
}

func (a *API) handleListWorkflows(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	list, err := a.svc.ListWorkflows(r.Context())
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := WorkflowListResponse{Workflows: make([]WorkflowResponse, 0, len(list))}
	for _, wf := range list {
		out.Workflows = append(out.Workflows, workflowResponse(wf))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetWorkflow(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	wf, err := a.svc.GetWorkflow(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, workflowResponse(wf))
}

func (a *API) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[UpdateWorkflowRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	wf, err := a.svc.UpdateWorkflow(r.Context(), sess.Principal, id, workflows.WorkflowUpdate{Name: req.Name, Description: req.Description})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, workflowResponse(wf))
}

func (a *API) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if err := a.svc.DeleteWorkflow(r.Context(), sess.Principal, id); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCreateWorkflowVersion(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[CreateWorkflowVersionRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if len(req.Document) == 0 {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "document", Message: "required"}))
		return
	}
	v, created, err := a.svc.CreateVersion(r.Context(), sess.Principal, id, req.Document)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, versionResponse(v))
}

func (a *API) handleListWorkflowVersions(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	list, err := a.svc.ListVersions(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := WorkflowVersionListResponse{Versions: make([]WorkflowVersionMetaResponse, 0, len(list))}
	for _, v := range list {
		out.Versions = append(out.Versions, WorkflowVersionMetaResponse{Version: int(v.Version), ContentHash: v.ContentHash, CreatedAt: v.CreatedAt})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetWorkflowVersion(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such workflow")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	ver, err := pathVersion(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	v, err := a.svc.GetVersion(r.Context(), id, ver)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, versionResponse(v))
}
