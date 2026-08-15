// Package api is identity's HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
)

// required:"true" on the fields below is read by swaggest/jsonschema-go and
// puts the property in the schema's `required` array; without it
// openapi-typescript renders every property optional. Only Plan 11's web
// slice is marked up — the rest of this file's DTOs are Plan 12's to do as
// those screens arrive. required and nullable are independent: Grants stays
// type ["array","null"] because the handler really can emit null.

type LoginRequest struct {
	Email    string `json:"email" required:"true"`
	Password string `json:"password" required:"true"`
}

type UserResponse struct {
	ID        string    `json:"id" required:"true"`
	Email     string    `json:"email" required:"true"`
	Role      string    `json:"role" required:"true"`
	Active    bool      `json:"active" required:"true"`
	CreatedAt time.Time `json:"created_at" required:"true"`
	UpdatedAt time.Time `json:"updated_at" required:"true"`
}

type GrantResponse struct {
	ProjectID string `json:"project_id" required:"true"`
	Level     string `json:"level" required:"true"`
}

type MeResponse struct {
	User   UserResponse    `json:"user" required:"true"`
	Grants []GrantResponse `json:"grants" required:"true"`
}

type UserListResponse struct {
	Users []UserResponse `json:"users"`
}

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type UpdateUserRequest struct {
	Role   *string `json:"role,omitempty"`
	Active *bool   `json:"active,omitempty"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type ProjectGrantResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Level  string `json:"level"`
}

type GrantListResponse struct {
	Grants []ProjectGrantResponse `json:"grants"`
}

type SetGrantRequest struct {
	UserID string `json:"user_id"`
	Level  string `json:"level"`
}

func userResponse(u identity.User) UserResponse {
	return UserResponse{
		ID: u.ID.String(), Email: u.Email, Role: string(u.Role), Active: u.Active,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}
