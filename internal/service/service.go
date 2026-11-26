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

// Drafts is next week's plan before it is confirmed: making it, reviewing
// it together (votes, swaps, Ask Claude) and confirming it. Every Claude
// call is queued as a job and returns at once; the result arrives as an
// events.Event. A failed job never changes existing data.
type Drafts interface {
	// Card is the state of the Week page's draft card.
	Card(ctx context.Context, s domain.Scope) (domain.DraftCard, error)
	// RequestDraft queues a draft job for the next shopping day. An existing
	// draft for that week is replaced once the new one is valid.
	RequestDraft(ctx context.Context, s domain.Scope) (domain.Job, error)
	// Review returns the newest draft with votes; domain.ErrNotFound if none.
	Review(ctx context.Context, s domain.Scope) (domain.Review, error)
	// DraftMeal returns one meal of the draft with its votes.
	DraftMeal(ctx context.Context, s domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error)
	// Vote sets the caller's vote on a draft meal: +1, -1, or 0 to clear it.
	Vote(ctx context.Context, s domain.Scope, mealID uuid.UUID, vote int) (domain.DraftMeal, error)
	// RequestSwap queues a swap job for one draft meal.
	RequestSwap(ctx context.Context, s domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error)
	// RemoveMeal deletes a meal from the draft.
	RemoveMeal(ctx context.Context, s domain.Scope, mealID uuid.UUID) error
	// RequestAsk queues an Ask Claude job: Claude returns the whole meal set
	// and the server applies the difference to the draft.
	RequestAsk(ctx context.Context, s domain.Scope, prompt string) (domain.Job, error)
	// ListPreview is the shopping list the draft would make. Never stored.
	ListPreview(ctx context.Context, s domain.Scope) (domain.List, error)
	// Confirm makes the draft the active plan and builds its list. It is
	// idempotent: a second confirm of the same draft returns the plan.
	Confirm(ctx context.Context, s domain.Scope) (domain.Plan, error)
}

// Outcomes is the rating step: what happened to each meal of the plan
// that is ending.
type Outcomes interface {
	// RateList is the active plan's meals with their outcomes;
	// domain.ErrNotFound without an active plan.
	RateList(ctx context.Context, s domain.Scope) (domain.RateList, error)
	// SetOutcome rates a meal (cooked), marks it not cooked, clears the
	// answer, or changes the note of an existing answer.
	SetOutcome(ctx context.Context, s domain.Scope, mealID uuid.UUID, in domain.OutcomeInput) (domain.RateMeal, error)
}

// Library is the household's recipes. Every planned meal links to a
// recipe, but only recipes the household cooked, saved from a meal, added
// or tweaked are in the library (in_library) and listed by Search. The
// rest still open by id (past plans, "not cooked last week").
type Library interface {
	// Search lists library recipes matching q (title or ingredient) and a
	// filter: "all", "rated4", "own" or a meal type.
	Search(ctx context.Context, s domain.Scope, q string, filter domain.RecipeFilter) ([]domain.RecipeSummary, error)
	RecipeDetail(ctx context.Context, s domain.Scope, recipeID uuid.UUID) (domain.RecipeDetail, error)
	// RequestTweak queues a tweak job: Claude changes the recipe and the
	// result is saved as a new recipe with parent_recipe_id set.
	RequestTweak(ctx context.Context, s domain.Scope, recipeID uuid.UUID, prompt string) (domain.Job, error)
	// AddToNextWeek adds the recipe to the draft, making an empty draft if
	// there is none. from is FromLibrary or FromNotCooked.
	AddToNextWeek(ctx context.Context, s domain.Scope, recipeID uuid.UUID, from domain.MealSource) (domain.DraftMeal, error)
	// AddToThisWeek puts the recipe's ingredients that are not on the
	// active list yet on it, unticked. It returns how many were added.
	AddToThisWeek(ctx context.Context, s domain.Scope, recipeID uuid.UUID) (int, error)
	// Import stores a photo, text or link as a draft and queues a job that
	// reads it with Claude; the draft is then checked and saved.
	Import(ctx context.Context, s domain.Scope, in domain.ImportInput) (domain.RecipeDraft, error)
	ImportDraft(ctx context.Context, s domain.Scope, draftID uuid.UUID) (domain.RecipeDraft, error)
	DiscardImport(ctx context.Context, s domain.Scope, draftID uuid.UUID) error
	// SaveImport validates the checked recipe, saves it to the library and
	// deletes the draft.
	SaveImport(ctx context.Context, s domain.Scope, draftID uuid.UUID, r domain.MealJSON) (domain.Recipe, error)
	// CreateRecipe saves a recipe written by hand.
	CreateRecipe(ctx context.Context, s domain.Scope, r domain.MealJSON) (domain.Recipe, error)
	// SaveMeal puts a meal's recipe in the library ("Save to recipes"),
	// linking the meal to a recipe first if it has none. Idempotent.
	SaveMeal(ctx context.Context, s domain.Scope, mealID uuid.UUID) (domain.Recipe, error)
	// SetPhoto stores (or replaces) the family's photo of a recipe; jpeg
	// is already resized by imagefit. PhotoAt is nil without a photo.
	SetPhoto(ctx context.Context, s domain.Scope, recipeID uuid.UUID, jpeg []byte) error
	Photo(ctx context.Context, s domain.Scope, recipeID uuid.UUID) (domain.RecipePhoto, error)
	PhotoAt(ctx context.Context, s domain.Scope, recipeID uuid.UUID) (*time.Time, error)
	DeletePhoto(ctx context.Context, s domain.Scope, recipeID uuid.UUID) error
}

// Lists is the shopping list of the active plan.
type Lists interface {
	Active(ctx context.Context, s domain.Scope) (domain.List, error)
	// Rebuild regenerates the generated items of a plan's list from its
	// meals, keeping ticks and manual items.
	Rebuild(ctx context.Context, s domain.Scope, planID uuid.UUID) (domain.List, error)
	AddItem(ctx context.Context, s domain.Scope, name, section string) (domain.ListItem, error)
	SetChecked(ctx context.Context, s domain.Scope, itemID uuid.UUID, checked bool) (domain.ListItem, error)
	// TickAt applies a tick made at a given time (an offline tick sent
	// later) unless a later tick of the item was already applied: the last
	// tick wins. It returns the item as it is now.
	TickAt(ctx context.Context, s domain.Scope, itemID uuid.UUID, checked bool, at time.Time) (domain.ListItem, error)
	// SetHaveIt answers a Check at home item: true is "Have it" (hidden
	// from shopping), false is "Buy" (shown in its section), nil clears the
	// answer. Items not under Check at home are domain.ErrNotFound.
	SetHaveIt(ctx context.Context, s domain.Scope, itemID uuid.UUID, haveIt *bool) (domain.ListItem, error)
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
	// AvoidItems is the avoid list, by name.
	AvoidItems(ctx context.Context, s domain.Scope) ([]domain.AvoidItem, error)
	// AddAvoid adds one name. A name already on the list (by normalized
	// name) returns the existing entry.
	AddAvoid(ctx context.Context, s domain.Scope, name string, source domain.AvoidSource) (domain.AvoidItem, error)
	DeleteAvoid(ctx context.Context, s domain.Scope, id uuid.UUID) error
	// Setup saves the four setup questions of a new household (meal
	// counts, people, never-eat foods, shopping day) and marks it set up.
	Setup(ctx context.Context, s domain.Scope, in domain.SetupInput) (domain.Profile, error)
}

// Feedback is ratings and the saved-recipe library.
type Feedback interface {
	Rate(ctx context.Context, s domain.Scope, mealID uuid.UUID, rating int, note string) (domain.Feedback, error)
	SaveRecipe(ctx context.Context, s domain.Scope, mealID uuid.UUID) (domain.Recipe, error)
	Recipes(ctx context.Context, s domain.Scope) ([]domain.Recipe, error)
}

// Households manages households, members and invites.
type Households interface {
	// Create makes a household with default profile and sections and a
	// one-time admin invite (Household.Invite). A non-empty adminEmail is
	// also added as admin (creating the member if needed). Server admin only.
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
	// RequestLoginLink emails a single-use sign-in link (15 minutes). It
	// answers the same whether or not the address has an account.
	RequestLoginLink(ctx context.Context, email string) error
	// SignInWithLink uses a link and returns a new session token. A link
	// sent from an invite adds the membership.
	SignInWithLink(ctx context.Context, linkToken string) (sessionToken string, s domain.Scope, err error)
	// InvitePreview is the join page's view of a usable invite.
	InvitePreview(ctx context.Context, inviteToken string) (domain.InvitePreview, error)
	// Join answers an invite with a name and email: it emails a sign-in
	// link that adds the membership when used.
	Join(ctx context.Context, inviteToken, name, email string) error
}

// Services bundles everything the API and web layers need.
type Services struct {
	Auth       Auth
	Households Households
	Profiles   Profiles
	Plans      Plans
	Drafts     Drafts
	Outcomes   Outcomes
	Library    Library
	Lists      Lists
	Feedback   Feedback
}
