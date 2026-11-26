package planner_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Only recipes the household cooked, saved or added are in the library;
// planned meals still link to a recipe that opens and can be added back.
func TestLibraryHoldsOnlyCookedOrSaved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// The second week plans the traybake again (rice was not cooked, so
	// it may not come back).
	next := reply(t,
		meal(domain.Monday, "Chicken traybake", []domain.IngredientJSON{ing("chicken thighs", 500, "g", "meat_fish")}),
		meal(domain.Tuesday, "Fish pie", []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")}),
	)
	d := newDraftEnv(t, weekA(t), next)
	plan := d.confirmed(t)
	traybake, rice := plan.Meals[0], plan.Meals[1]
	if traybake.RecipeID == nil || rice.RecipeID == nil {
		t.Fatalf("planned meals are not linked to recipes: %+v", plan.Meals)
	}

	// A planned week adds nothing to the library.
	if got, err := d.plans.Search(ctx, d.a, "", domain.FilterAll); err != nil || len(got) != 0 {
		t.Fatalf("library after planning: %+v %v", got, err)
	}
	if m, err := d.plans.Meal(ctx, d.a, rice.ID); err != nil || m.InLibrary {
		t.Errorf("meal before saving: in library %v, %v", m.InLibrary, err)
	}
	// The recipe still opens and can be added to next week.
	if det, err := d.plans.RecipeDetail(ctx, d.a, *rice.RecipeID); err != nil || det.InLibrary || det.Meal.Title != "Onion rice" {
		t.Errorf("recipe outside the library: %+v %v", det.Recipe, err)
	}

	// Rated as cooked: in the library. Not cooked: not.
	if _, err := d.plans.SetOutcome(ctx, d.a, traybake.ID, domain.OutcomeInput{Rating: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.SetOutcome(ctx, d.a, rice.ID, domain.OutcomeInput{NotCooked: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.plans.Search(ctx, d.a, "", domain.FilterAll)
	if len(got) != 1 || got[0].ID != *traybake.RecipeID || got[0].Source.Label() != "" {
		t.Fatalf("library after rating: %+v", got)
	}

	// Save to recipes: in the library, the same recipe, idempotent.
	for range 2 {
		rec, err := d.plans.SaveMeal(ctx, d.a, rice.ID)
		if err != nil || rec.ID != *rice.RecipeID || !rec.InLibrary {
			t.Fatalf("save meal: %+v %v", rec, err)
		}
	}
	if got, _ = d.plans.Search(ctx, d.a, "", domain.FilterAll); len(got) != 2 {
		t.Errorf("library after saving: %+v", got)
	}
	if m, _ := d.plans.Meal(ctx, d.a, rice.ID); !m.InLibrary {
		t.Error("saved meal not marked in library")
	}

	// Another household: not found, and nothing changes.
	if _, err := d.plans.SaveMeal(ctx, d.b, traybake.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("other household saves: %v", err)
	}
	if got, _ := d.plans.Search(ctx, d.b, "", domain.FilterAll); len(got) != 0 {
		t.Errorf("B sees A's library: %+v", got)
	}

	// The next plan is told the library's titles and reuses the recipe.
	d.draft(t)
	prompt := d.fake.Requests[1].Messages[0].Content
	if !strings.Contains(prompt, "## The household's recipes") || !strings.Contains(prompt, `dinner: "Chicken traybake"`) {
		t.Errorf("prompt lacks the library:\n%s", prompt)
	}
	if strings.Contains(prompt, `dinner: "Onion rice"`) {
		t.Error("a meal not cooked last week is offered for reuse")
	}
	if sys := d.fake.Requests[1].System; !strings.Contains(sys, "served with rugbrød") {
		t.Errorf("system prompt does not rule out default sides:\n%s", sys)
	}
	r, _ := d.plans.Review(ctx, d.a)
	for _, m := range r.Meals {
		if m.Title == "Chicken traybake" && (m.RecipeID == nil || *m.RecipeID != *traybake.RecipeID) {
			t.Errorf("planned again but not linked to the library recipe: %v", m.RecipeID)
		}
	}
}

// Recipes the household adds go straight into the library, labelled
// "Our own"; a tweaked version joins it too.
func TestAddedRecipesAreInLibrary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	soup := reply(t,
		meal(domain.Monday, "Our  fish soup", []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")}),
		meal(domain.Tuesday, "Onion rice", []domain.IngredientJSON{ing("rice", 200, "g", "dry_goods")}),
	)
	d := newDraftEnv(t, soup)
	rec, err := d.plans.CreateRecipe(ctx, d.a, domain.MealJSON{
		MealType: domain.MealDinner, Title: "Our fish soup", Protein: domain.ProteinFish, Minutes: 30, Servings: 3,
		Ingredients: []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")},
		Steps:       []domain.StepJSON{{Text: "Cook it."}},
	})
	if err != nil || !rec.InLibrary {
		t.Fatalf("create: %+v %v", rec, err)
	}
	own, _ := d.plans.Search(ctx, d.a, "", domain.FilterOwn)
	if len(own) != 1 || own[0].Source.Label() != "Our own" {
		t.Errorf("own: %+v", own)
	}

	// A plan that reuses the title links the meal to the family's recipe.
	r := d.draft(t)
	if !strings.Contains(d.fake.Requests[0].Messages[0].Content, `dinner: "Our fish soup"`) {
		t.Error("prompt lacks the family's recipe")
	}
	for _, m := range r.Meals {
		if m.Title == "Our  fish soup" && (m.RecipeID == nil || *m.RecipeID != rec.ID) {
			t.Errorf("reused title not linked to the library recipe: %v", m.RecipeID)
		}
	}
	if all, _ := d.plans.Search(ctx, d.a, "", domain.FilterAll); len(all) != 1 {
		t.Errorf("planning added to the library: %+v", all)
	}
}
