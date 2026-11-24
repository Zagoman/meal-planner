package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Unimplemented satisfies every service interface and returns
// domain.ErrNotImplemented. Packages embed it while they are being built so
// the server compiles and runs from day one.
type Unimplemented struct{}

var (
	_ Plans      = Unimplemented{}
	_ Drafts     = Unimplemented{}
	_ Outcomes   = Unimplemented{}
	_ Library    = Unimplemented{}
	_ Lists      = Unimplemented{}
	_ Profiles   = Unimplemented{}
	_ Feedback   = Unimplemented{}
	_ Households = Unimplemented{}
	_ Auth       = Unimplemented{}
)

// NewUnimplemented returns a Services where every method is unimplemented.
func NewUnimplemented() Services {
	u := Unimplemented{}
	return Services{Auth: u, Households: u, Profiles: u, Plans: u, Drafts: u, Outcomes: u, Library: u, Lists: u, Feedback: u}
}

var errNI = domain.ErrNotImplemented

func (Unimplemented) Generate(context.Context, domain.Scope, string) (domain.Plan, error) {
	return domain.Plan{}, errNI
}
func (Unimplemented) Current(context.Context, domain.Scope) (domain.Plan, error) {
	return domain.Plan{}, errNI
}
func (Unimplemented) SwapMeal(context.Context, domain.Scope, uuid.UUID, domain.Weekday, string) (domain.Meal, error) {
	return domain.Meal{}, errNI
}
func (Unimplemented) Substitute(context.Context, domain.Scope, uuid.UUID, string, string) (domain.Meal, error) {
	return domain.Meal{}, errNI
}
func (Unimplemented) Meal(context.Context, domain.Scope, uuid.UUID) (domain.Meal, error) {
	return domain.Meal{}, errNI
}
func (Unimplemented) Active(context.Context, domain.Scope) (domain.List, error) {
	return domain.List{}, errNI
}
func (Unimplemented) Rebuild(context.Context, domain.Scope, uuid.UUID) (domain.List, error) {
	return domain.List{}, errNI
}
func (Unimplemented) AddItem(context.Context, domain.Scope, string, string) (domain.ListItem, error) {
	return domain.ListItem{}, errNI
}
func (Unimplemented) SetChecked(context.Context, domain.Scope, uuid.UUID, bool) (domain.ListItem, error) {
	return domain.ListItem{}, errNI
}
func (Unimplemented) SetHaveIt(context.Context, domain.Scope, uuid.UUID, *bool) (domain.ListItem, error) {
	return domain.ListItem{}, errNI
}
func (Unimplemented) DeleteItem(context.Context, domain.Scope, uuid.UUID) error { return errNI }
func (Unimplemented) Get(context.Context, domain.Scope) (domain.Profile, error) {
	return domain.Profile{}, errNI
}
func (Unimplemented) Put(context.Context, domain.Scope, domain.Profile) (domain.Profile, error) {
	return domain.Profile{}, errNI
}
func (Unimplemented) Sections(context.Context, domain.Scope) ([]domain.Section, error) {
	return nil, errNI
}
func (Unimplemented) PutSections(context.Context, domain.Scope, []domain.Section) ([]domain.Section, error) {
	return nil, errNI
}
func (Unimplemented) Pantry(context.Context, domain.Scope) ([]domain.PantryItem, error) {
	return nil, errNI
}
func (Unimplemented) PutPantry(context.Context, domain.Scope, []string) ([]domain.PantryItem, error) {
	return nil, errNI
}
func (Unimplemented) AvoidItems(context.Context, domain.Scope) ([]domain.AvoidItem, error) {
	return nil, errNI
}
func (Unimplemented) AddAvoid(context.Context, domain.Scope, string, domain.AvoidSource) (domain.AvoidItem, error) {
	return domain.AvoidItem{}, errNI
}
func (Unimplemented) DeleteAvoid(context.Context, domain.Scope, uuid.UUID) error { return errNI }
func (Unimplemented) Rate(context.Context, domain.Scope, uuid.UUID, int, string) (domain.Feedback, error) {
	return domain.Feedback{}, errNI
}
func (Unimplemented) SaveRecipe(context.Context, domain.Scope, uuid.UUID) (domain.Recipe, error) {
	return domain.Recipe{}, errNI
}
func (Unimplemented) Recipes(context.Context, domain.Scope) ([]domain.Recipe, error) {
	return nil, errNI
}
func (Unimplemented) Create(context.Context, domain.Scope, string, string) (domain.Household, error) {
	return domain.Household{}, errNI
}
func (Unimplemented) Members(context.Context, domain.Scope) ([]domain.Member, error) {
	return nil, errNI
}
func (Unimplemented) Memberships(context.Context, domain.Scope) ([]domain.Membership, error) {
	return nil, errNI
}
func (Unimplemented) CreateInvite(context.Context, domain.Scope, domain.Role) (domain.Invite, error) {
	return domain.Invite{}, errNI
}
func (Unimplemented) AcceptInvite(context.Context, uuid.UUID, string) (domain.Membership, error) {
	return domain.Membership{}, errNI
}
func (Unimplemented) Usage(context.Context, domain.Scope, time.Time) ([]domain.UsageRow, error) {
	return nil, errNI
}
func (Unimplemented) Login(context.Context, string, string) (string, domain.Scope, error) {
	return "", domain.Scope{}, errNI
}
func (Unimplemented) Logout(context.Context, string) error { return errNI }
func (Unimplemented) SessionScope(context.Context, string) (domain.Scope, error) {
	return domain.Scope{}, errNI
}
func (Unimplemented) BearerScope(context.Context, string) (domain.Scope, error) {
	return domain.Scope{}, errNI
}
func (Unimplemented) SwitchHousehold(context.Context, string, uuid.UUID) (domain.Scope, error) {
	return domain.Scope{}, errNI
}
func (Unimplemented) CreateAPIToken(context.Context, domain.Scope, string) (string, error) {
	return "", errNI
}

func (Unimplemented) Card(context.Context, domain.Scope) (domain.DraftCard, error) {
	return domain.DraftCard{}, errNI
}
func (Unimplemented) RequestDraft(context.Context, domain.Scope) (domain.Job, error) {
	return domain.Job{}, errNI
}
func (Unimplemented) Review(context.Context, domain.Scope) (domain.Review, error) {
	return domain.Review{}, errNI
}
func (Unimplemented) DraftMeal(context.Context, domain.Scope, uuid.UUID) (domain.DraftMeal, error) {
	return domain.DraftMeal{}, errNI
}
func (Unimplemented) Vote(context.Context, domain.Scope, uuid.UUID, int) (domain.DraftMeal, error) {
	return domain.DraftMeal{}, errNI
}
func (Unimplemented) RequestSwap(context.Context, domain.Scope, uuid.UUID) (domain.DraftMeal, error) {
	return domain.DraftMeal{}, errNI
}
func (Unimplemented) RemoveMeal(context.Context, domain.Scope, uuid.UUID) error { return errNI }
func (Unimplemented) RequestAsk(context.Context, domain.Scope, string) (domain.Job, error) {
	return domain.Job{}, errNI
}
func (Unimplemented) ListPreview(context.Context, domain.Scope) (domain.List, error) {
	return domain.List{}, errNI
}
func (Unimplemented) Confirm(context.Context, domain.Scope) (domain.Plan, error) {
	return domain.Plan{}, errNI
}
func (Unimplemented) RateList(context.Context, domain.Scope) (domain.RateList, error) {
	return domain.RateList{}, errNI
}
func (Unimplemented) SetOutcome(context.Context, domain.Scope, uuid.UUID, domain.OutcomeInput) (domain.RateMeal, error) {
	return domain.RateMeal{}, errNI
}
func (Unimplemented) Search(context.Context, domain.Scope, string, domain.RecipeFilter) ([]domain.RecipeSummary, error) {
	return nil, errNI
}
func (Unimplemented) RecipeDetail(context.Context, domain.Scope, uuid.UUID) (domain.RecipeDetail, error) {
	return domain.RecipeDetail{}, errNI
}
func (Unimplemented) RequestTweak(context.Context, domain.Scope, uuid.UUID, string) (domain.Job, error) {
	return domain.Job{}, errNI
}
func (Unimplemented) AddToNextWeek(context.Context, domain.Scope, uuid.UUID, domain.MealSource) (domain.DraftMeal, error) {
	return domain.DraftMeal{}, errNI
}
func (Unimplemented) AddToThisWeek(context.Context, domain.Scope, uuid.UUID) (int, error) {
	return 0, errNI
}
