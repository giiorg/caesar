// Package api is assets' HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/assets"
)

type ProjectResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProjectListResponse struct {
	Projects []ProjectResponse `json:"projects"`
}

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UpdateProjectRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type RepoResponse struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Forge         string    `json:"forge"`
	Slug          string    `json:"slug"`
	DefaultBranch string    `json:"default_branch"`
	CredentialID  string    `json:"credential_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RepoListResponse struct {
	Repos []RepoResponse `json:"repos"`
}

type CreateRepoRequest struct {
	Forge         string `json:"forge"`
	Slug          string `json:"slug"`
	DefaultBranch string `json:"default_branch,omitempty"`
	CredentialID  string `json:"credential_id"`
}

type UpdateRepoRequest struct {
	Slug          *string `json:"slug,omitempty"`
	DefaultBranch *string `json:"default_branch,omitempty"`
	CredentialID  *string `json:"credential_id,omitempty"`
}

func projectResponse(p assets.Project) ProjectResponse {
	return ProjectResponse{
		ID: p.ID.String(), Name: p.Name, Description: p.Description,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func repoResponse(r assets.Repo) RepoResponse {
	return RepoResponse{
		ID: r.ID.String(), ProjectID: r.ProjectID.String(), Forge: string(r.Forge), Slug: r.Slug,
		DefaultBranch: r.DefaultBranch, CredentialID: r.CredentialID.String(),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
