package domain

import (
	"time"

	"github.com/google/uuid"
)

// Weekday is a lowercase English day name, as stored in the database and
// returned by Claude.
type Weekday string

const (
	Monday    Weekday = "monday"
	Tuesday   Weekday = "tuesday"
	Wednesday Weekday = "wednesday"
	Thursday  Weekday = "thursday"
	Friday    Weekday = "friday"
	Saturday  Weekday = "saturday"
	Sunday    Weekday = "sunday"
)

// Weekdays in plan order.
var Weekdays = []Weekday{Monday, Tuesday, Wednesday, Thursday, Friday, Saturday, Sunday}

func (d Weekday) Valid() bool {
	for _, w := range Weekdays {
		if d == w {
			return true
		}
	}
	return false
}

// Protein is the main protein of a meal.
type Protein string

const (
	ProteinChicken    Protein = "chicken"
	ProteinFish       Protein = "fish"
	ProteinVegetarian Protein = "vegetarian"
	ProteinOther      Protein = "other"
)

type Household struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Member struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        Role      `json:"role,omitempty"`
	ServerAdmin bool      `json:"server_admin,omitempty"`
}

// Membership is one household a member belongs to (for switching).
type Membership struct {
	HouseholdID   uuid.UUID `json:"household_id"`
	HouseholdName string    `json:"household_name"`
	Role          Role      `json:"role"`
}

type Invite struct {
	ID         uuid.UUID  `json:"id"`
	Role       Role       `json:"role"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	// Token is only set when the invite is created; only its hash is stored.
	Token string `json:"token,omitempty"`
	URL   string `json:"url,omitempty"`
}

type SpiceLevel string

const (
	SpiceNone   SpiceLevel = "none"
	SpiceMild   SpiceLevel = "mild"
	SpiceMedium SpiceLevel = "medium"
	SpiceHot    SpiceLevel = "hot"
)

// Profile is a household's standing preferences, sent with every prompt.
type Profile struct {
	DinnersPerWeek int             `json:"dinners_per_week"`
	Servings       int             `json:"servings"`
	SpiceLevel     SpiceLevel      `json:"spice_level"`
	OnePot         bool            `json:"one_pot"`
	ProteinMix     map[Protein]int `json:"protein_mix"` // dinners per week by protein
	Avoid          []string        `json:"avoid"`
	Shop           string          `json:"shop"`
	ShoppingDay    Weekday         `json:"shopping_day"`
	BabyPortions   bool            `json:"baby_portions"`
	Language       string          `json:"language"`
}

// Section is a shop section, e.g. {Key: "meat_fish", Name: "Meat & fish"}.
type Section struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// PantryCheckSection is the synthetic last section for items the household
// usually has at home.
const PantryCheckSection = "pantry_check"

// DefaultSections are created for every new household.
var DefaultSections = []Section{
	{Key: "produce", Name: "Fruit & veg", SortOrder: 10},
	{Key: "bakery", Name: "Bakery", SortOrder: 20},
	{Key: "meat_fish", Name: "Meat & fish", SortOrder: 30},
	{Key: "dairy_eggs", Name: "Dairy & eggs", SortOrder: 40},
	{Key: "dry_goods", Name: "Pasta, rice & grains", SortOrder: 50},
	{Key: "tins_jars", Name: "Tins & jars", SortOrder: 60},
	{Key: "spices", Name: "Spices & baking", SortOrder: 70},
	{Key: "frozen", Name: "Frozen", SortOrder: 80},
	{Key: "other", Name: "Other", SortOrder: 90},
}

type PantryItem struct {
	Name           string `json:"name"`
	NormalizedName string `json:"normalized_name"`
}

type PlanStatus string

const (
	PlanDraft    PlanStatus = "draft"
	PlanActive   PlanStatus = "active"
	PlanArchived PlanStatus = "archived"
)

type Plan struct {
	ID        uuid.UUID  `json:"id"`
	WeekStart time.Time  `json:"week_start"`
	Status    PlanStatus `json:"status"`
	Prompt    string     `json:"prompt"`
	CreatedAt time.Time  `json:"created_at"`
	Meals     []Meal     `json:"meals"`
}

type Meal struct {
	ID               uuid.UUID    `json:"id"`
	Day              Weekday      `json:"day"`
	Title            string       `json:"title"`
	Protein          Protein      `json:"protein"`
	Minutes          int          `json:"minutes"`
	OnePot           bool         `json:"one_pot"`
	Servings         int          `json:"servings"`
	Ingredients      []Ingredient `json:"ingredients"`
	Steps            []Step       `json:"steps"`
	AfterBabyPortion []string     `json:"after_baby_portion"`
}

type Ingredient struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	NormalizedName string    `json:"normalized_name"`
	Qty            float64   `json:"qty"`
	Unit           string    `json:"unit"`
	Section        string    `json:"section"`
}

type Step struct {
	Text        string `json:"text"`
	TimerMin    *int   `json:"timer_min,omitempty"`
	BabyPortion bool   `json:"baby_portion"`
}

type ListItem struct {
	ID        uuid.UUID  `json:"id"`
	PlanID    uuid.UUID  `json:"plan_id"`
	Name      string     `json:"name"`
	QtyText   string     `json:"qty_text"`  // buying unit, "1 net"
	NeedText  string     `json:"need_text"` // "need 3"
	Section   string     `json:"section"`
	Checked   bool       `json:"checked"`
	CheckedBy *uuid.UUID `json:"checked_by,omitempty"`
	IsManual  bool       `json:"is_manual"`
}

// ListSection is one group of the shopping list, in the household's order.
type ListSection struct {
	Key   string     `json:"key"`
	Name  string     `json:"name"`
	Items []ListItem `json:"items"`
}

// List is the active plan's shopping list, grouped and ordered for display.
type List struct {
	PlanID   uuid.UUID     `json:"plan_id"`
	Sections []ListSection `json:"sections"`
}

type Feedback struct {
	ID        uuid.UUID `json:"id"`
	MealID    uuid.UUID `json:"meal_id"`
	Rating    int       `json:"rating"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type Recipe struct {
	ID           uuid.UUID  `json:"id"`
	SourceMealID *uuid.UUID `json:"source_meal_id,omitempty"`
	Meal         Meal       `json:"meal"`
	CreatedAt    time.Time  `json:"created_at"`
}

// UsageRow is token usage for one household over a period.
type UsageRow struct {
	HouseholdID  uuid.UUID `json:"household_id"`
	Household    string    `json:"household"`
	Calls        int64     `json:"calls"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
}
