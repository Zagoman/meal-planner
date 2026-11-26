package household_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/db"
)

// Rating a meal (cooked) and saving it put its recipe in the library;
// another household's rating changes nothing.
func TestRateAndSavePutRecipeInLibrary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	inLibrary := func(mealID uuid.UUID) bool {
		t.Helper()
		m, err := f.q.GetMeal(ctx, db.GetMealParams{ID: mealID, HouseholdID: f.a.HouseholdID})
		if err != nil || !m.RecipeID.Valid {
			t.Fatalf("meal recipe: %+v %v", m, err)
		}
		r, err := f.q.GetRecipe(ctx, db.GetRecipeParams{ID: m.RecipeID.UUID, HouseholdID: f.a.HouseholdID})
		if err != nil {
			t.Fatal(err)
		}
		return r.InLibrary
	}
	// A planned meal linked to a recipe that is not in the library.
	rated := f.meal(t, f.a)
	id, err := f.q.UpsertClaudeRecipe(ctx, db.UpsertClaudeRecipeParams{
		HouseholdID: f.a.HouseholdID, Title: "Fish pie", Protein: "fish", Minutes: 40, Servings: 3,
		Data: []byte(`{}`), MealType: "dinner", TitleKey: "fish pie",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q.SetMealRecipe(ctx, db.SetMealRecipeParams{ID: rated, HouseholdID: f.a.HouseholdID, RecipeID: uuid.NullUUID{UUID: id, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if inLibrary(rated) {
		t.Fatal("planned recipe starts in the library")
	}
	if _, err := f.s.Rate(ctx, f.b, rated, 5, ""); err == nil {
		t.Error("B rated A's meal")
	}
	if inLibrary(rated) {
		t.Error("B's rating put A's recipe in the library")
	}
	if _, err := f.s.Rate(ctx, f.a, rated, 4, ""); err != nil {
		t.Fatal(err)
	}
	if !inLibrary(rated) {
		t.Error("cooked meal's recipe not in the library")
	}
	// SaveRecipe returns it as in the library.
	r, err := f.s.SaveRecipe(ctx, f.a, rated)
	if err != nil || !r.InLibrary || r.ID != id {
		t.Errorf("saved recipe: %+v %v", r, err)
	}
}
