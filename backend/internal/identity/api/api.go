package api

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// SessionCookie aliases the shared pinned cookie name (platform/authn).
const SessionCookie = authn.SessionCookie

type API struct {
	svc        *identity.Service
	sessionTTL time.Duration // cookie Max-Age; server-side expiry is authoritative
}

func New(svc *identity.Service, sessionTTL time.Duration) *API {
	return &API{svc: svc, sessionTTL: sessionTTL}
}

// Operations is the single route table: mux registration (httpx.Mount)
// and OpenAPI generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/auth/login", ID: "login", Summary: "Authenticate and set the session cookie",
			Request: LoginRequest{}, Response: UserResponse{}, Status: http.StatusOK,
			Handler: http.HandlerFunc(a.handleLogin),
		},
		{
			Pattern: "POST /api/v1/auth/logout", ID: "logout", Summary: "Revoke the current session",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleLogout),
		},
		{
			Pattern: "GET /api/v1/auth/me", ID: "me", Summary: "Current user with project grants",
			Response: MeResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleMe),
		},
		{
			Pattern: "PUT /api/v1/auth/password", ID: "changePassword", Summary: "Change own password (revokes all sessions, rotates the cookie)",
			Request: ChangePasswordRequest{}, Status: http.StatusNoContent,
			Handler: a.authenticated(a.handleChangePassword),
		},
		{
			Pattern: "POST /api/v1/users", ID: "createUser", Summary: "Create a user (admin)",
			Request: CreateUserRequest{}, Response: UserResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateUser),
		},
		{
			Pattern: "GET /api/v1/users", ID: "listUsers", Summary: "List users (admin)",
			Response: UserListResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleListUsers),
		},
		{
			Pattern: "GET /api/v1/users/{id}", ID: "getUser", Summary: "Get a user (admin)",
			Response: UserResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleGetUser),
		},
		{
			Pattern: "PATCH /api/v1/users/{id}", ID: "updateUser", Summary: "Update role or active flag (admin)",
			Request: UpdateUserRequest{}, Response: UserResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleUpdateUser),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess identity.AuthSession)

func (a *API) authenticated(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassUnauthenticated, "authentication required"))
			return
		}
		sess, err := a.svc.AuthenticateSession(r.Context(), c.Value)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		next(w, r, sess)
	})
}

func (a *API) adminOnly(next sessionHandler) http.Handler {
	return a.authenticated(func(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
		if sess.Role != identity.RoleAdmin {
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

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassNotFound, "no such user")
	}
	return id, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *API) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/",
		MaxAge: int(a.sessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	req, err := decode[LoginRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	user, token, err := a.svc.Login(r.Context(), req.Email, req.Password, clientIP(r))
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	a.setSessionCookie(w, token)
	httpx.JSON(w, http.StatusOK, userResponse(user))
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	if err := a.svc.Logout(r.Context(), sess); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	user, err := a.svc.GetUser(r.Context(), sess.UserID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	grants, err := a.svc.GrantsForUser(r.Context(), sess.UserID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := MeResponse{User: userResponse(user), Grants: make([]GrantResponse, 0, len(grants))}
	for _, g := range grants {
		out.Grants = append(out.Grants, GrantResponse{ProjectID: g.ProjectID.String(), Level: string(g.Level)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	req, err := decode[ChangePasswordRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	token, err := a.svc.ChangePassword(r.Context(), sess, req.CurrentPassword, req.NewPassword)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	a.setSessionCookie(w, token)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	req, err := decode[CreateUserRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	user, err := a.svc.CreateUser(r.Context(), sess.Principal, req.Email, req.Password, identity.Role(req.Role))
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, userResponse(user))
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request, _ identity.AuthSession) {
	users, err := a.svc.ListUsers(r.Context())
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := UserListResponse{Users: make([]UserResponse, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, userResponse(u))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetUser(w http.ResponseWriter, r *http.Request, _ identity.AuthSession) {
	id, err := pathID(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	user, err := a.svc.GetUser(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, userResponse(user))
}

func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	id, err := pathID(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[UpdateUserRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	upd := identity.UserUpdate{Active: req.Active}
	if req.Role != nil {
		role := identity.Role(*req.Role)
		upd.Role = &role
	}
	user, err := a.svc.UpdateUser(r.Context(), sess.Principal, id, upd)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, userResponse(user))
}
