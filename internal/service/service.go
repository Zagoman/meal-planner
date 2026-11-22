// Package service defines the one service layer that both the JSON API
// (/api/*) and the web UI call, so business rules live in one place.
//
// Rules for every implementation:
//   - Every method takes the caller's domain.Scope and every query filters by
//     scope.HouseholdID. A row from another household is reported as
//     domain.ErrNotFound, never as forbidden, so IDs cannot be probed.
//   - Errors wrap the sentinels in package domain.
//   - Methods that change the shopping list publish an events.Event for the
//     household after the change commits.
//
// Ownership: Plans and Lists are implemented in internal/planner and
// internal/shopping (package B); Auth, Households, Profiles and Feedback in
// internal/auth and internal/household (package A).
package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Plans generates and changes the week's meals.
type Plans interface {
	// Generate asks Claude for a new plan from the household profile, recent
	// feedback and prompt. The new plan becomes active only once it is fully
	// validated and saved; on any failure the previous plan stays active and
	// the error wraps domain.ErrGeneration. It also rebuilds the list.
	Generate(ctx context.Context, s domain.Scope, prompt string) (domain.Plan, error)
	// Current returns the active plan with meals, ingredients and steps.
	Current(ctx context.Context, s domain.Scope) (domain.Plan, error)
	// SwapMeal replaces one day's meal. Manual list items and ticks are kept.
	SwapMeal(ctx context.Context, s domain.Scope, planID uuid.UUID, day domain.Weekday, prompt string) (domain.Meal, error)
	// Substitute replaces one ingredient in a meal (no Claude call needed
	// unless the implementation chooses to) and rebuilds the list.
	Substitute(ctx context.Context, s domain.Scope, mealID uuid.UUID, from, to string) (domain.Meal, error)
	// Meal returns one meal of the household (recipe screen).
	Meal(ctx context.Context, s domain.Scope, mealID uuid.UUID) (domain.Meal, error)
}

// Lists is the shopping list of the active plan.
type Lists interface {
	Active(ctx context.Context, s domain.Scope) (domain.List, error)
	// Rebuild regenerates the generated items of a plan's list from its
	// meals, keeping ticks and manual items.
	Rebuild(ctx context.Context, s domain.Scope, planID uuid.UUID) (domain.List, error)
	AddItem(ctx context.Context, s domain.Scope, name, section string) (domain.ListItem, error)
	SetChecked(ctx context.Context, s domain.Scope, itemID uuid.UUID, checked bool) (domain.ListItem, error)
	DeleteItem(ctx context.Context, s domain.Scope, itemID uuid.UUID) error
}

// Profiles is the household's preferences, shop sections and pantry.
type Profiles interface {
	Get(ctx context.Context, s domain.Scope) (domain.Profile, error)
	Put(ctx context.Context, s domain.Scope, p domain.Profile) (domain.Profile, error)
	Sections(ctx context.Context, s domain.Scope) ([]domain.Section, error)
	PutSections(ctx context.Context, s domain.Scope, sections []domain.Section) ([]domain.Section, error)
	Pantry(ctx context.Context, s domain.Scope) ([]domain.PantryItem, error)
	PutPantry(ctx context.Context, s domain.Scope, names []string) ([]domain.PantryItem, error)
}

// Feedback is ratings and the saved-recipe library.
type Feedback interface {
	Rate(ctx context.Context, s domain.Scope, mealID uuid.UUID, rating int, note string) (domain.Feedback, error)
	SaveRecipe(ctx context.Context, s domain.Scope, mealID uuid.UUID) (domain.Recipe, error)
	Recipes(ctx context.Context, s domain.Scope) ([]domain.Recipe, error)
}

// Households manages households, members and invites.
type Households interface {
	// Create makes a household with default profile and sections and adds
	// adminEmail as its admin (creating the member if needed). Server admin only.
	Create(ctx context.Context, s domain.Scope, name, adminEmail string) (domain.Household, error)
	Members(ctx context.Context, s domain.Scope) ([]domain.Member, error)
	Memberships(ctx context.Context, s domain.Scope) ([]domain.Membership, error)
	// CreateInvite returns an invite whose Token/URL is shown once. Household admin only.
	CreateInvite(ctx context.Context, s domain.Scope, role domain.Role) (domain.Invite, error)
	// AcceptInvite adds the member to the invite's household. The token is
	// the only authority; it is single use and expires.
	AcceptInvite(ctx context.Context, memberID uuid.UUID, token string) (domain.Membership, error)
	// Usage is token usage per household since a time. Server admin only.
	Usage(ctx context.Context, s domain.Scope, since time.Time) ([]domain.UsageRow, error)
}

// Auth turns credentials into a Scope. Raw tokens are never stored, only
// their SHA-256 hashes.
type Auth interface {
	Login(ctx context.Context, email, password string) (sessionToken string, s domain.Scope, err error)
	Logout(ctx context.Context, sessionToken string) error
	SessionScope(ctx context.Context, sessionToken string) (domain.Scope, error)
	BearerScope(ctx context.Context, apiToken string) (domain.Scope, error)
	SwitchHousehold(ctx context.Context, sessionToken string, householdID uuid.UUID) (domain.Scope, error)
	CreateAPIToken(ctx context.Context, s domain.Scope, name string) (token string, err error)
}

// Services bundles everything the API and web layers need.
type Services struct {
	Auth       Auth
	Households Households
	Profiles   Profiles
	Plans      Plans
	Lists      Lists
	Feedback   Feedback
}
