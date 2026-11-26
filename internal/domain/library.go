package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// OutcomeStatus is what happened to a planned meal.
type OutcomeStatus string

const (
	Cooked    OutcomeStatus = "cooked"
	NotCooked OutcomeStatus = "not_cooked"
)

// MealOutcome is the household's answer for one meal: cooked with a rating
// (1–5) or not cooked. A meal counts as cooked only once someone rates it.
type MealOutcome struct {
	MealID   uuid.UUID     `json:"meal_id"`
	Status   OutcomeStatus `json:"status"`
	Rating   int           `json:"rating,omitempty"` // 0 when not cooked
	Note     string        `json:"note,omitempty"`
	MemberID *uuid.UUID    `json:"member_id,omitempty"`
	At       time.Time     `json:"at"`
}

// OutcomeInput sets a meal's outcome: a rating (cooked), NotCooked, or
// Clear to remove the answer. A note alone updates an existing outcome.
type OutcomeInput struct {
	Rating    int
	NotCooked bool
	Clear     bool
	Note      *string
}

// RateMeal is one meal of the rating list.
type RateMeal struct {
	Meal
	Outcome *MealOutcome `json:"outcome,omitempty"`
}

// RateList is the plan that just ended, meal by meal.
type RateList struct {
	Plan  Plan       `json:"plan"`
	Meals []RateMeal `json:"meals"`
}

// Done counts meals with an answer.
func (l RateList) Done() int {
	n := 0
	for _, m := range l.Meals {
		if m.Outcome != nil {
			n++
		}
	}
	return n
}

// RecipeSource is where a library recipe came from.
type RecipeSource string

const (
	SourceClaude RecipeSource = "claude"
	SourcePhoto  RecipeSource = "photo"
	SourceText   RecipeSource = "text"
	SourceLink   RecipeSource = "link"
	SourceManual RecipeSource = "manual"
)

// Label is "Planned" (made with the plans) or "Ours". It never names the AI provider.
func (s RecipeSource) Label() string {
	if s == SourceClaude || s == "" {
		return "Planned"
	}
	return "Ours"
}

// RecipeStats are a recipe's ratings across every time it was planned.
type RecipeStats struct {
	Rating      float64 `json:"rating"` // average, 0 if never rated
	Ratings     int     `json:"ratings"`
	TimesCooked int     `json:"times_cooked"`
	LatestNote  string  `json:"latest_note,omitempty"`
}

// RecipeSummary is one row of the library.
type RecipeSummary struct {
	ID       uuid.UUID    `json:"id"`
	Title    string       `json:"title"`
	MealType MealType     `json:"meal_type"`
	Minutes  int          `json:"minutes"`
	Source   RecipeSource `json:"source"`
	Tweaked  bool         `json:"tweaked"`
	RecipeStats
}

// RecipeFilter narrows the library.
type RecipeFilter string

const (
	FilterAll    RecipeFilter = "all"
	FilterRated4 RecipeFilter = "rated4"
	FilterOwn    RecipeFilter = "own"
	FilterOfType RecipeFilter = "" // a meal type name, e.g. "dinner"
)

// RecipeDetail is a library recipe with its stats and tweak state.
type RecipeDetail struct {
	Recipe
	RecipeStats
	// Tweak is the latest tweak job of this recipe, if any; TweakedID the
	// new version it made.
	Tweak     *Job       `json:"tweak,omitempty"`
	TweakedID *uuid.UUID `json:"tweaked_id,omitempty"`
	// PhotoAt is when the family's photo was added; nil shows the drawing.
	PhotoAt *time.Time `json:"photo_at,omitempty"`
}

// RecipePhoto is the family's photo of a recipe, a JPEG.
type RecipePhoto struct {
	JPEG      []byte
	UpdatedAt time.Time
}

// NotCookedMeal is a meal of the plan that ended, marked not cooked, that
// can be added back to the draft.
type NotCookedMeal struct {
	RecipeID uuid.UUID `json:"recipe_id"`
	Title    string    `json:"title"`
	MealType MealType  `json:"meal_type"`
}

// TitleKey is how recipe titles are compared: lowercase, single spaces.
func TitleKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// SetupInput is the four setup questions of a new household.
type SetupInput struct {
	MealTargets  map[MealType]int
	People       int
	BabyPortions bool
	Avoid        []string
	ShoppingDay  Weekday
	Cuisines     []string // optional, values of Cuisines
}

// ImportSource is where an imported recipe came from.
type ImportSource string

const (
	ImportPhoto ImportSource = "photo"
	ImportText  ImportSource = "text"
	ImportLink  ImportSource = "link"
)

// ImportInput is a recipe to import: a photo, pasted text or a link.
type ImportInput struct {
	Source    ImportSource
	Text      string // pasted text, or the link
	Photo     []byte // already resized
	PhotoType string // "image/jpeg"
}

// RecipeDraft is an imported recipe waiting to be checked.
type RecipeDraft struct {
	ID        uuid.UUID    `json:"id"`
	Source    ImportSource `json:"source"`
	RawInput  string       `json:"raw_input"`
	Recipe    *MealJSON    `json:"recipe,omitempty"` // nil until read
	Uncertain []string     `json:"uncertain"`
	// Job is the import job: queued or running while Claude reads it.
	Job *Job `json:"job,omitempty"`
}

// Reading reports whether Claude is still reading the recipe.
func (d RecipeDraft) Reading() bool {
	return d.Recipe == nil && (d.Job == nil || d.Job.Status.Pending())
}
