package domain

import (
	_ "embed"
	"encoding/json"
)

// mealPlanSchema is the JSON schema Claude's output must match. Package
// planner passes it to the Claude API (structured output / tool input schema)
// and validates responses against it.
//
//go:embed meal_plan.schema.json
var mealPlanSchema []byte

// recipeImportSchema is what Claude returns when it reads a recipe from a
// photo, text or web page.
//
//go:embed recipe_import.schema.json
var recipeImportSchema []byte

// RecipeImportSchema returns a copy of the recipe import JSON schema.
func RecipeImportSchema() json.RawMessage {
	out := make([]byte, len(recipeImportSchema))
	copy(out, recipeImportSchema)
	return out
}

// RecipeImportJSON is the decoded form of an import answer.
type RecipeImportJSON struct {
	Recipe    MealJSON `json:"recipe"`
	Uncertain []string `json:"uncertain"`
}

// MealPlanSchema returns a copy of the meal plan JSON schema.
func MealPlanSchema() json.RawMessage {
	out := make([]byte, len(mealPlanSchema))
	copy(out, mealPlanSchema)
	return out
}

// MealPlanJSON is the decoded form of Claude's response.
type MealPlanJSON struct {
	Meals []MealJSON `json:"meals"`
}

type MealJSON struct {
	MealType         MealType         `json:"meal_type"`
	Day              *Weekday         `json:"day"` // suggested day, null for none
	Title            string           `json:"title"`
	Protein          Protein          `json:"protein"`
	Minutes          int              `json:"minutes"`
	OnePot           bool             `json:"one_pot"`
	Servings         int              `json:"servings"`
	Ingredients      []IngredientJSON `json:"ingredients"`
	Steps            []StepJSON       `json:"steps"`
	AfterBabyPortion []string         `json:"after_baby_portion"`
}

type IngredientJSON struct {
	Name           string  `json:"name"`
	NormalizedName string  `json:"normalized_name"`
	Qty            float64 `json:"qty"`
	Unit           string  `json:"unit"`
	Section        string  `json:"section"`
}

type StepJSON struct {
	Text        string `json:"text"`
	TimerMin    *int   `json:"timer_min"`
	BabyPortion bool   `json:"baby_portion"`
}
