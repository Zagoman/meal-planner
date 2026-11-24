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

// MealType is what a meal is for. Claude plans breakfast, lunch, dinner and
// baking; "other" is for meals added by hand.
type MealType string

const (
	MealBreakfast MealType = "breakfast"
	MealLunch     MealType = "lunch"
	MealDinner    MealType = "dinner"
	MealBaking    MealType = "baking"
	MealOther     MealType = "other"
)

// PlannedMealTypes are the types a household sets targets for, in display order.
var PlannedMealTypes = []MealType{MealBreakfast, MealLunch, MealDinner, MealBaking}

func (t MealType) Valid() bool {
	switch t {
	case MealBreakfast, MealLunch, MealDinner, MealBaking, MealOther:
		return true
	}
	return false
}

// Label is the display name, "Breakfast".
func (t MealType) Label() string {
	switch t {
	case MealBreakfast:
		return "Breakfast"
	case MealLunch:
		return "Lunch"
	case MealDinner:
		return "Dinner"
	case MealBaking:
		return "Baking"
	}
	return "Other"
}

// MaxMealsPerType is the highest target for one meal type (two a day).
const MaxMealsPerType = 14

// MealSource says how a meal got into a plan.
type MealSource string

const (
	FromClaude    MealSource = "claude"
	FromLibrary   MealSource = "library"
	FromNotCooked MealSource = "not_cooked"
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
	// MealTargets is how many meals of each planned type a week holds
	// (0..MaxMealsPerType each, at least one meal in total).
	MealTargets map[MealType]int `json:"meal_targets"`
	People      int              `json:"people"`
	SpiceLevel  SpiceLevel       `json:"spice_level"`
	OnePot      bool             `json:"one_pot"`
	ProteinMix  map[Protein]int  `json:"protein_mix"` // dinners per week by protein
	// Avoid is the names on the avoid list (avoid_items). Profiles.Put
	// replaces the list with these names; see Profiles.AddAvoid for one.
	Avoid           []string `json:"avoid"`
	Shop            string   `json:"shop"`
	ShoppingDay     Weekday  `json:"shopping_day"`
	AutoConfirmTime string   `json:"auto_confirm_time"` // "08:00", household time zone
	TimeZone        string   `json:"time_zone"`         // IANA name, default Europe/Copenhagen
	BabyPortions    bool     `json:"baby_portions"`
	Language        string   `json:"language"`
}

// TotalMeals is the sum of the meal targets.
func (p Profile) TotalMeals() int {
	n := 0
	for _, t := range PlannedMealTypes {
		n += p.MealTargets[t]
	}
	return n
}

// AvoidSource says where an avoid-list entry was added.
type AvoidSource string

const (
	AvoidFromSetup   AvoidSource = "setup"
	AvoidFromRecipe  AvoidSource = "recipe"
	AvoidFromProfile AvoidSource = "profile"
)

// AvoidItem is one entry of the avoid list: an ingredient never planned.
type AvoidItem struct {
	ID             uuid.UUID   `json:"id"`
	Name           string      `json:"name"`
	NormalizedName string      `json:"normalized_name"`
	Source         AvoidSource `json:"source"`
	AddedAt        time.Time   `json:"added_at"`
}

// Section is a shop section, e.g. {Key: "meat_fish", Name: "Meat & fish"}.
type Section struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// PantryCheckSection is the key of the synthetic "Check at home" group at
// the top of the list: items on the household's pantry that are still
// unanswered. It is never stored as a list item's section.
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

// Plan is a week of meals. A "draft" is waiting for review; "active" is
// the confirmed plan the shopping list belongs to.
type Plan struct {
	ID            uuid.UUID  `json:"id"`
	WeekStart     time.Time  `json:"week_start"`
	Status        PlanStatus `json:"status"`
	Prompt        string     `json:"prompt"`
	CreatedAt     time.Time  `json:"created_at"`
	ConfirmedAt   *time.Time `json:"confirmed_at,omitempty"`
	AutoConfirmed bool       `json:"auto_confirmed"`
	// ConfirmNote explains an auto-confirm that kept disliked meals.
	ConfirmNote string `json:"confirm_note,omitempty"`
	Meals       []Meal `json:"meals"`
}

type Meal struct {
	ID               uuid.UUID    `json:"id"`
	MealType         MealType     `json:"meal_type"`
	Day              Weekday      `json:"day,omitempty"` // suggested day; "" for none
	AddedFrom        MealSource   `json:"added_from"`
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
	// CheckHome marks a pantry item: it is shown under Check at home until
	// someone answers Have it (HaveIt true: hidden from shopping) or Buy
	// (HaveIt false: shown in Section).
	CheckHome bool  `json:"check_home"`
	HaveIt    *bool `json:"have_it,omitempty"`
}

// ToBuy reports whether the item is part of the shopping, i.e. counts
// towards progress: everything except unanswered or "Have it" pantry items.
func (it ListItem) ToBuy() bool {
	return !it.CheckHome || (it.HaveIt != nil && !*it.HaveIt)
}

// ListSection is one group of the shopping list, in the household's order.
type ListSection struct {
	Key   string     `json:"key"`
	Name  string     `json:"name"`
	Items []ListItem `json:"items"`
}

// List is the active plan's shopping list, grouped and ordered for display.
// CheckHome holds every pantry item with its Have it / Buy answer, shown at
// the top; Sections hold everything to buy, including pantry items answered
// Buy.
type List struct {
	PlanID    uuid.UUID     `json:"plan_id"`
	CheckHome []ListItem    `json:"check_home"`
	Sections  []ListSection `json:"sections"`
}

// CheckHomeAnswered counts the Check at home items with an answer.
func (l List) CheckHomeAnswered() int {
	n := 0
	for _, it := range l.CheckHome {
		if it.HaveIt != nil {
			n++
		}
	}
	return n
}

// Progress counts the items to buy and how many are ticked.
func (l List) Progress() (done, total int) {
	for _, s := range l.Sections {
		for _, it := range s.Items {
			total++
			if it.Checked {
				done++
			}
		}
	}
	return done, total
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
