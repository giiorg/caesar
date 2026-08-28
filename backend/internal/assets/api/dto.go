// Package api is assets' HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/assets"
)

// ProjectResponse and RepoResponse are required throughout, for the reason the
// global constraint gives: the new-task form's project → repo cascade reads
// these and an unmarked property renders optional in TypeScript. Both mappers
// set every field unconditionally and neither struct carries an omitempty, so
// no shape of either omits a key.
type ProjectResponse struct {
	ID          string    `json:"id" required:"true"`
	Name        string    `json:"name" required:"true"`
	Description string    `json:"description" required:"true"`
	CreatedAt   time.Time `json:"created_at" required:"true"`
	UpdatedAt   time.Time `json:"updated_at" required:"true"`
}

type ProjectListResponse struct {
	Projects []ProjectResponse `json:"projects" required:"true"`
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
	ID            string    `json:"id" required:"true"`
	ProjectID     string    `json:"project_id" required:"true"`
	Forge         string    `json:"forge" required:"true"`
	Slug          string    `json:"slug" required:"true"`
	DefaultBranch string    `json:"default_branch" required:"true"`
	CredentialID  string    `json:"credential_id" required:"true"`
	CreatedAt     time.Time `json:"created_at" required:"true"`
	UpdatedAt     time.Time `json:"updated_at" required:"true"`
}

type RepoListResponse struct {
	Repos []RepoResponse `json:"repos" required:"true"`
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
//
// The three nullable properties are required AND nullable, which is the
// honest pair: "never used", "never auth-expired" and "this kind has no
// fingerprint" are answers the credentials screen renders, not keys it
// fails to find. That is why none of them carries omitempty — omitempty
// contradicts required, and a client told a property is guaranteed and
// then handed a body without it is reading a bug.
type CredentialResponse struct {
	ID            string    `json:"id" required:"true"`
	Name          string    `json:"name" required:"true"`
	Kind          string    `json:"kind" required:"true"`
	KeyID         string    `json:"key_id" required:"true"`
	SecretVersion int64     `json:"secret_version" required:"true"`
	ProvidedAt    time.Time `json:"provided_at" required:"true"`
	// Fingerprint is populated ONLY for agent_auth_file (it feeds the partial
	// unique custody index; every other kind stores NULL) — required AND
	// nullable, the honest pair.
	Fingerprint       *string    `json:"fingerprint" required:"true"`
	LastUsedAt        *time.Time `json:"last_used_at" required:"true"`
	LastAuthExpiredAt *time.Time `json:"last_auth_expired_at" required:"true"`
	CreatedAt         time.Time  `json:"created_at" required:"true"`
	UpdatedAt         time.Time  `json:"updated_at" required:"true"`
}

type CredentialListResponse struct {
	Credentials []CredentialResponse `json:"credentials" required:"true"`
}

type CreateCredentialRequest struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Secret string `json:"secret"`
}

type RotateCredentialSecretRequest struct {
	Secret string `json:"secret"`
}

// Defaults is required and nullable for the reason AuditEntryResponse.Detail
// is: a Go map is always marshaled, and a nil one marshals to null. The key
// is therefore always on the wire, carrying either an object or null.
type HarnessResponse struct {
	ID               string         `json:"id" required:"true"`
	Name             string         `json:"name" required:"true"`
	Kind             string         `json:"kind" required:"true"`
	ImageRef         string         `json:"image_ref" required:"true"`
	ProviderEndpoint string         `json:"provider_endpoint" required:"true"`
	CredentialID     string         `json:"credential_id" required:"true"`
	Defaults         map[string]any `json:"defaults" required:"true"`
	CreatedAt        time.Time      `json:"created_at" required:"true"`
	UpdatedAt        time.Time      `json:"updated_at" required:"true"`
}

type HarnessListResponse struct {
	Harnesses []HarnessResponse `json:"harnesses" required:"true"`
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
		SecretVersion: c.SecretVersion, ProvidedAt: c.UpdatedAt, Fingerprint: c.Fingerprint,
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
