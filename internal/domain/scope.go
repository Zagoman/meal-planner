// Package domain holds the types shared by every layer: the caller's scope,
// the household's data, and the meal JSON contract with Claude.
package domain

import (
	"context"

	"github.com/google/uuid"
)

// Role is a member's role inside one household.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Scope is who is calling and on behalf of which household. Every service
// method takes one, and every query it runs filters by Scope.HouseholdID.
// Auth middleware (session cookie or bearer token) is the only code that
// creates a Scope.
type Scope struct {
	HouseholdID uuid.UUID
	MemberID    uuid.UUID
	Role        Role
	ServerAdmin bool
}

// IsHouseholdAdmin reports whether the caller may manage the household.
func (s Scope) IsHouseholdAdmin() bool { return s.Role == RoleAdmin }

type scopeKey struct{}

// WithScope returns a context carrying the caller's scope.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom returns the caller's scope, or false if the request is anonymous.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok && s.HouseholdID != uuid.Nil && s.MemberID != uuid.Nil
}
