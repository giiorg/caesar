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

// CredentialResponse is metadata only — the secret never leaves the
// control plane. ProvidedAt is when the operator last supplied a secret
// value; together with the two freshness stamps it is what an operator
// reads to decide whether a subscription credential needs re-importing.
type CredentialResponse struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Kind              string     `json:"kind"`
	KeyID             string     `json:"key_id"`
	SecretVersion     int64      `json:"secret_version"`
	ProvidedAt        time.Time  `json:"provided_at"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	LastAuthExpiredAt *time.Time `json:"last_auth_expired_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type CredentialListResponse struct {
	Credentials []CredentialResponse `json:"credentials"`
}

type CreateCredentialRequest struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Secret string `json:"secret"`
}

type RotateCredentialSecretRequest struct {
	Secret string `json:"secret"`
}

type HarnessResponse struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Kind             string         `json:"kind"`
	ImageRef         string         `json:"image_ref"`
	ProviderEndpoint string         `json:"provider_endpoint"`
	CredentialID     string         `json:"credential_id"`
	Defaults         map[string]any `json:"defaults"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type HarnessListResponse struct {
	Harnesses []HarnessResponse `json:"harnesses"`
}

type RegisterHarnessRequest struct {
	Name             string         `json:"name"`
	Kind             string         `json:"kind"`
	ImageRef         string         `json:"image_ref"`
	ProviderEndpoint string         `json:"provider_endpoint"`
	CredentialID     string         `json:"credential_id"`
	Defaults         map[string]any `json:"defaults,omitempty"`
}

type UpdateHarnessRequest struct {
	Name             *string         `json:"name,omitempty"`
	ImageRef         *string         `json:"image_ref,omitempty"`
	ProviderEndpoint *string         `json:"provider_endpoint,omitempty"`
	CredentialID     *string         `json:"credential_id,omitempty"`
	Defaults         *map[string]any `json:"defaults,omitempty"`
}

func credentialResponse(c assets.Credential) CredentialResponse {
	return CredentialResponse{
		ID: c.ID.String(), Name: c.Name, Kind: string(c.Kind), KeyID: c.KeyID,
		SecretVersion: c.SecretVersion, ProvidedAt: c.UpdatedAt,
		LastUsedAt: c.LastUsedAt, LastAuthExpiredAt: c.LastAuthExpiredAt,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func harnessResponse(h assets.Harness) HarnessResponse {
	return HarnessResponse{
		ID: h.ID.String(), Name: h.Name, Kind: string(h.Kind), ImageRef: h.ImageRef,
		ProviderEndpoint: h.ProviderEndpoint, CredentialID: h.CredentialID.String(), Defaults: h.Defaults,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}
