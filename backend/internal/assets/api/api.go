package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// GrantSource lists a member's grants for project-visibility filtering.
// identity.Service satisfies it; app wiring passes it in.
type GrantSource interface {
	GrantsForUser(ctx context.Context, userID uuid.UUID) ([]authn.Grant, error)
}

type API struct {
	svc    *assets.Service
	auth   authn.SessionAuthenticator
	authz  authn.Authorizer
	grants GrantSource
}

func New(svc *assets.Service, auth authn.SessionAuthenticator, authz authn.Authorizer, grants GrantSource) *API {
	return &API{svc: svc, auth: auth, authz: authz, grants: grants}
}

// Operations is assets' route table: mux registration (httpx.Mount) and
// OpenAPI generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/projects", ID: "createProject", Summary: "Create a project (admin)",
			Request: CreateProjectRequest{}, Response: ProjectResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateProject),
		},
		{
			Pattern: "GET /api/v1/projects", ID: "listProjects", Summary: "List projects visible to the caller",
			Response: ProjectListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListProjects),
		},
		{
			Pattern: "GET /api/v1/projects/{id}", ID: "getProject", Summary: "Get a project (view)",
			Response: ProjectResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetProject),
		},
		{
			Pattern: "PATCH /api/v1/projects/{id}", ID: "updateProject", Summary: "Update name or description (manage)",
			Request: UpdateProjectRequest{}, Response: ProjectResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleUpdateProject),
		},
		{
			Pattern: "DELETE /api/v1/projects/{id}", ID: "deleteProject", Summary: "Delete an empty project (manage)",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleDeleteProject),
		},
		{
			Pattern: "GET /api/v1/projects/{id}/repos", ID: "listProjectRepos", Summary: "List a project's repos (view)",
			Response: RepoListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListProjectRepos),
		},
		{
			Pattern: "POST /api/v1/projects/{id}/repos", ID: "createRepo", Summary: "Register a repo on a project (manage)",
			Request: CreateRepoRequest{}, Response: RepoResponse{}, Status: http.StatusCreated,
			Handler: a.authenticated(a.handleCreateRepo),
		},
		{
			Pattern: "GET /api/v1/repos/{id}", ID: "getRepo", Summary: "Get a repo (view)",
			Response: RepoResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetRepo),
		},
		{
			Pattern: "PATCH /api/v1/repos/{id}", ID: "updateRepo", Summary: "Update a repo (manage)",
			Request: UpdateRepoRequest{}, Response: RepoResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleUpdateRepo),
		},
		{
			Pattern: "DELETE /api/v1/repos/{id}", ID: "deleteRepo", Summary: "Delete a repo (manage)",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleDeleteRepo),
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

func (a *API) adminOnly(next sessionHandler) http.Handler {
	return a.authenticated(func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
		if sess.Role != authn.RoleAdmin {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "admin role required"))
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

// decode mirrors identity/api's strict decoder (contexts never share
// hand-rolled helpers across the boundary; ten lines beats a new seam).
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

func parseUUIDField(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: field, Message: "must be a uuid"})
	}
	return id, nil
}

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateProjectRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	p, err := a.svc.CreateProject(r.Context(), sess.Principal, req.Name, req.Description)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, projectResponse(p))
}

func (a *API) handleListProjects(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	var (
		projects []assets.Project
		err      error
	)
	if sess.Role == authn.RoleAdmin {
		projects, err = a.svc.ListProjects(r.Context())
	} else {
		var grants []authn.Grant
		grants, err = a.grants.GrantsForUser(r.Context(), sess.UserID)
		if err == nil {
			ids := make([]uuid.UUID, 0, len(grants))
			for _, g := range grants {
				ids = append(ids, g.ProjectID)
			}
			projects, err = a.svc.ListProjectsByIDs(r.Context(), ids)
		}
	}
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := ProjectListResponse{Projects: make([]ProjectResponse, 0, len(projects))}
	for _, p := range projects {
		out.Projects = append(out.Projects, projectResponse(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantView) {
		return
	}
	p, err := a.svc.GetProject(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, projectResponse(p))
}

func (a *API) handleUpdateProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantManage) {
		return
	}
	req, err := decode[UpdateProjectRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	p, err := a.svc.UpdateProject(r.Context(), sess.Principal, id, assets.ProjectUpdate{Name: req.Name, Description: req.Description})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, projectResponse(p))
}

func (a *API) handleDeleteProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantManage) {
		return
	}
	if err := a.svc.DeleteProject(r.Context(), sess.Principal, id); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListProjectRepos(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantView) {
		return
	}
	repos, err := a.svc.ListRepos(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := RepoListResponse{Repos: make([]RepoResponse, 0, len(repos))}
	for _, repo := range repos {
		out.Repos = append(out.Repos, repoResponse(repo))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleCreateRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantManage) {
		return
	}
	req, err := decode[CreateRepoRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	credID, err := parseUUIDField(req.CredentialID, "credential_id")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	repo, err := a.svc.CreateRepo(r.Context(), sess.Principal, id, assets.NewRepo{
		Forge: assets.ForgeKind(req.Forge), Slug: req.Slug, DefaultBranch: req.DefaultBranch, CredentialID: credID,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, repoResponse(repo))
}

// repoScoped resolves {id} to a repo and enforces min on its project.
func (a *API) repoScoped(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, min authn.GrantLevel) (assets.Repo, bool) {
	id, err := pathUUID(r, "no such repo")
	if err != nil {
		httpx.Problem(w, r, err)
		return assets.Repo{}, false
	}
	repo, err := a.svc.GetRepo(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return assets.Repo{}, false
	}
	if !a.authorize(w, r, sess, repo.ProjectID, min) {
		return assets.Repo{}, false
	}
	return repo, true
}

func (a *API) handleGetRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	repo, ok := a.repoScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, repoResponse(repo))
}

func (a *API) handleUpdateRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	repo, ok := a.repoScoped(w, r, sess, authn.GrantManage)
	if !ok {
		return
	}
	req, err := decode[UpdateRepoRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	upd := assets.RepoUpdate{Slug: req.Slug, DefaultBranch: req.DefaultBranch}
	if req.CredentialID != nil {
		credID, err := parseUUIDField(*req.CredentialID, "credential_id")
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		upd.CredentialID = &credID
	}
	updated, err := a.svc.UpdateRepo(r.Context(), sess.Principal, repo.ID, upd)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, repoResponse(updated))
}

func (a *API) handleDeleteRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	repo, ok := a.repoScoped(w, r, sess, authn.GrantManage)
	if !ok {
		return
	}
	if err := a.svc.DeleteRepo(r.Context(), sess.Principal, repo.ID); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
