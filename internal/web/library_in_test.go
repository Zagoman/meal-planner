package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Planned meals are not in Recipes until they are cooked or saved with
// "Save to recipes" on the meal page; their recipe still opens.
func TestSaveToRecipes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna, carl := f.session(f.seed.Anna), f.session(f.seed.Carl)
	m := f.seed.HomePlan.Meals[0]
	save := "/meals/" + m.ID.String() + "/save"

	// Nothing planned is in the library yet: no rows, no "Planned", only the
	// chips that list something ("All").
	rec := f.do(t, req{path: "/recipes", session: anna})
	body := rec.Body.String()
	mustContain(t, "empty library", body, "No recipes found", `value="all"`)
	for _, no := range []string{m.Title, "Planned", `value="rated4"`, `value="own"`, `value="dinner"`, `value="baking"`} {
		if strings.Contains(body, no) {
			t.Errorf("empty library shows %q", no)
		}
	}

	// The meal page offers Save to recipes; the planned recipe still opens.
	rec = f.do(t, req{path: "/meals/" + m.ID.String(), session: anna})
	mustContain(t, "meal page", rec.Body.String(), `id="save-recipe"`, `hx-post="`+save+`"`, "Save to recipes")
	got, err := f.store.Meal(context.Background(), f.seed.Anna, m.ID)
	if err != nil || got.RecipeID == nil || got.InLibrary {
		t.Fatalf("meal before saving: %+v %v", got, err)
	}
	if rec = f.do(t, req{path: "/recipes/" + got.RecipeID.String(), session: anna}); rec.Code != http.StatusOK {
		t.Errorf("recipe outside the library: %d", rec.Code)
	}

	// Another household: 404, nothing saved.
	if rec = f.do(t, req{method: "POST", path: save, session: carl, hx: true}); rec.Code != http.StatusNotFound {
		t.Errorf("other household saves: %d", rec.Code)
	}
	if lib, _ := f.store.Search(context.Background(), f.seed.Anna, "", domain.FilterAll); len(lib) != 0 {
		t.Errorf("other household's save changed the library: %+v", lib)
	}

	// Save (htmx): the partial says it is in Recipes. Saving again is fine.
	for range 2 {
		rec = f.do(t, req{method: "POST", path: save, session: anna, hx: true})
		mustContain(t, "saved", rec.Body.String(), `id="save-recipe"`, "In your recipes", "/recipes/"+got.RecipeID.String())
	}
	// Without JavaScript: back to the meal page, which now says it is saved.
	if rec = f.do(t, req{method: "POST", path: save, session: anna}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/meals/"+m.ID.String() {
		t.Errorf("no-JS save: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = f.do(t, req{path: "/meals/" + m.ID.String(), session: anna})
	if b := rec.Body.String(); !strings.Contains(b, "In your recipes") || strings.Contains(b, ">Save to recipes<") {
		t.Error("meal page after saving still offers Save to recipes")
	}

	// Recipes lists it, without a "Planned" tag, with its meal type's chip.
	rec = f.do(t, req{path: "/recipes", session: anna})
	body = rec.Body.String()
	mustContain(t, "library", body, m.Title, `value="`+string(m.MealType)+`"`)
	for _, no := range []string{"Planned", `<span class="src">`, `value="own"`} {
		if strings.Contains(body, no) {
			t.Errorf("library shows %q", no)
		}
	}
	if rec = f.do(t, req{path: "/recipes?q=" + url.QueryEscape(m.Title), session: carl, hx: true}); strings.Contains(rec.Body.String(), m.Title) {
		t.Error("other household's library shows the saved recipe")
	}
}

// A meal rated as cooked joins Recipes; one marked not cooked does not.
func TestCookedMealJoinsRecipes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	p := f.seed.HomePlan
	cooked, skipped := p.Meals[0], p.Meals[1]
	f.do(t, req{method: "POST", path: "/meals/" + cooked.ID.String() + "/outcome", form: url.Values{"rating": {"5"}}, session: anna, hx: true})
	f.do(t, req{method: "POST", path: "/meals/" + skipped.ID.String() + "/outcome", form: url.Values{"not_cooked": {"1"}}, session: anna, hx: true})
	rec := f.do(t, req{path: "/recipes", session: anna})
	body := rec.Body.String()
	mustContain(t, "library", body, cooked.Title, `value="rated4"`)
	if cooked.Title != skipped.Title && strings.Contains(body, skipped.Title) {
		t.Errorf("not-cooked meal %q is in Recipes", skipped.Title)
	}
	// The not-cooked meal's page still opens.
	if rec = f.do(t, req{path: "/meals/" + skipped.ID.String(), session: anna}); rec.Code != http.StatusOK {
		t.Errorf("not-cooked meal page: %d", rec.Code)
	}
}
