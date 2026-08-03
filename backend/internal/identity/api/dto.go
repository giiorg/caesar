// Package api is identity's HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
)

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GrantResponse struct {
	ProjectID string `json:"project_id"`
	Level     string `json:"level"`
}

type MeResponse struct {
	User   UserResponse    `json:"user"`
	Grants []GrantResponse `json:"grants"`
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
