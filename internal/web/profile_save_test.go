package web_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/web/webfake"
)

// Handoff item 8: Preferences save field by field.

func TestProfilePrefsAutosaveMarkup(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	body := f.do(t, req{path: "/profile", session: f.session(f.seed.Anna)}).Body.String()
	mustContain(t, "profile", body,
		`hx-post="/profile"`, `hx-swap="none"`, `hx-trigger="change"`, `hx-trigger="input changed delay:400ms"`,
		`data-autosave="shopping_day"`, `aria-describedby="err-people"`, `id="err-people"`,
		"Dinners per week by protein (optional)", `id="protein-help"`, "5 of 5 dinners set",
		`aria-describedby="protein-help err-protein"`,
		"<noscript>", "Save preferences", // only without JavaScript
		`<option value="en"`, ">English<", `<option value="da"`, ">Danish<",
		">Chicken", ">Mild<", ">None<")
	// The Save button is only inside <noscript>.
	if i := strings.Index(body, "Save preferences"); i < 0 || !strings.Contains(body[:i], "<noscript>") {
		t.Error("Save preferences outside <noscript>")
	}
}

func TestProfileFieldSaveMergesOneField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	anna := f.session(f.seed.Anna)
	p, _ := f.store.Get(ctx, f.seed.Anna)
	p.Avoid = nil
	p.Cuisines, p.BabyPortions, p.People = []string{"Greek", "Indian"}, true, 3
	if _, err := f.store.Put(ctx, f.seed.Anna, p); err != nil {
		t.Fatal(err)
	}
	// The form also carries values for other fields (a stale page, or one
	// still being typed): only the named field is saved.
	form := url.Values{"field": {"shopping_day"}, "shopping_day": {"friday"}, "people": {"9"}, "meals_dinner": {"1"}}
	rec := f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "saved", rec.Body.String(), `id="flash" hx-swap-oob="innerHTML"`, ">Saved<", `id="err-shopping_day"`)
	if strings.Contains(rec.Body.String(), `id="prefs"`) {
		t.Error("a field save swapped the whole form")
	}
	got, _ := f.store.Get(ctx, f.seed.Anna)
	if got.ShoppingDay != domain.Friday || got.People != 3 || got.MealTargets[domain.MealDinner] != 5 ||
		!got.BabyPortions || strings.Join(got.Cuisines, ",") != "Greek,Indian" {
		t.Errorf("after shopping_day: %+v", got)
	}

	// Unticking a checkbox sends no value: still saved as off, cuisines kept.
	rec = f.do(t, req{method: "POST", path: "/profile", form: url.Values{"field": {"baby_portions"}, "cuisine": {"Thai"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("baby portions: %d %s", rec.Code, rec.Body)
	}
	got, _ = f.store.Get(ctx, f.seed.Anna)
	if got.BabyPortions || strings.Join(got.Cuisines, ",") != "Greek,Indian" {
		t.Errorf("after baby_portions: %+v", got)
	}

	// Cuisine chips save as a set; other fields stay.
	rec = f.do(t, req{method: "POST", path: "/profile", form: url.Values{"field": {"cuisine"}, "cuisine": {"Thai", "Greek"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("cuisine: %d %s", rec.Code, rec.Body)
	}
	got, _ = f.store.Get(ctx, f.seed.Anna)
	if !slices.Equal(got.Cuisines, []string{"Thai", "Greek"}) || got.ShoppingDay != domain.Friday || got.People != 3 {
		t.Errorf("after cuisine: %+v", got)
	}

	// The avoid list is not touched by a preferences save.
	if len(got.Avoid) != len(p.Avoid) && p.Avoid != nil {
		t.Errorf("avoid changed: %v", got.Avoid)
	}

	// Language is a select of English and Danish.
	f.do(t, req{method: "POST", path: "/profile", form: url.Values{"field": {"language"}, "language": {"da"}}, session: anna, hx: true})
	if got, _ = f.store.Get(ctx, f.seed.Anna); got.Language != "da" {
		t.Errorf("language: %q", got.Language)
	}
}

func TestProfileFieldSaveErrorUnderField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	anna := f.session(f.seed.Anna)
	before, _ := f.store.Get(ctx, f.seed.Anna)

	rec := f.do(t, req{method: "POST", path: "/profile", form: url.Values{"field": {"people"}, "people": {"0"}}, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("people 0: %d", rec.Code)
	}
	mustContain(t, "people 0", rec.Body.String(), `id="err-people"`, `hx-swap-oob="true"`, "Must be 1 to 12.", "Not saved")
	if got, _ := f.store.Get(ctx, f.seed.Anna); got.People != before.People {
		t.Errorf("people saved: %d", got.People)
	}

	rec = f.do(t, req{method: "POST", path: "/profile", form: url.Values{"field": {"nope"}}, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown field: %d", rec.Code)
	}
}

func TestProfileProteinAboveDinnersRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	anna := f.session(f.seed.Anna)
	before, _ := f.store.Get(ctx, f.seed.Anna) // 5 dinners, split 2+1+2

	form := url.Values{"field": {"protein"}, "protein_chicken": {"4"}, "protein_fish": {"3"}}
	rec := f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("7 of 5: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "7 of 5", rec.Body.String(), `id="err-protein"`, "more than the 5 dinners a week", `id="protein-help"`, "7 of 5 dinners set")
	if got, _ := f.store.Get(ctx, f.seed.Anna); got.ProteinMix[domain.ProteinChicken] != before.ProteinMix[domain.ProteinChicken] {
		t.Errorf("protein saved: %v", got.ProteinMix)
	}

	// Fewer dinners than the split is refused too, under Dinner.
	rec = f.do(t, req{method: "POST", path: "/profile", form: url.Values{"field": {"meals_dinner"}, "meals_dinner": {"3"}}, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("dinners 3: %d", rec.Code)
	}
	mustContain(t, "dinners 3", rec.Body.String(), `id="err-meals_dinner"`)

	// A split at or under the dinners saves; the rest is up to us.
	form = url.Values{"field": {"protein"}, "protein_fish": {"2"}}
	rec = f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("2 of 5: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "2 of 5", rec.Body.String(), "2 of 5 dinners set · the rest is up to us")
	if got, _ := f.store.Get(ctx, f.seed.Anna); len(got.ProteinMix) != 1 || got.ProteinMix[domain.ProteinFish] != 2 {
		t.Errorf("protein: %v", got.ProteinMix)
	}
}

// Without JavaScript the whole form posts; a problem shows the page again
// with what was typed and the reason under the field.
func TestProfileNoScriptSave(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	form := url.Values{
		"meals_breakfast": {"0"}, "meals_lunch": {"0"}, "meals_dinner": {"4"}, "meals_baking": {"0"},
		"people": {"0"}, "spice_level": {"mild"}, "shop": {"Føtex"}, "shopping_day": {"monday"},
		"auto_confirm_time": {"08:00"}, "language": {"en"}, "cuisine": {"Greek"},
	}
	rec := f.do(t, req{method: "POST", path: "/profile", form: form, session: anna})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no-js invalid: %d", rec.Code)
	}
	mustContain(t, "no-js invalid", rec.Body.String(), `id="err-people"`, "Must be 1 to 12.", `value="Føtex"`, `value="Greek" checked`,
		`aria-invalid="true"`)
	form.Set("people", "2")
	rec = f.do(t, req{method: "POST", path: "/profile", form: form, session: anna})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no-js save: %d %s", rec.Code, rec.Body)
	}
	if got, _ := f.store.Get(context.Background(), f.seed.Anna); got.Shop != "Føtex" || got.People != 2 {
		t.Errorf("no-js: %+v", got)
	}
}

// Rule 1: a save only ever changes the signed-in household's profile.
func TestProfileFieldSaveIsolation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	annaBefore, _ := f.store.Get(ctx, f.seed.Anna)
	carl := f.session(f.seed.Carl)
	form := url.Values{"field": {"shop"}, "shop": {"Carl's shop"}, "household_id": {f.seed.Home.String()}}
	if rec := f.do(t, req{method: "POST", path: "/profile", form: form, session: carl, hx: true}); rec.Code != http.StatusOK {
		t.Fatalf("carl: %d", rec.Code)
	}
	if got, _ := f.store.Get(ctx, f.seed.Anna); got.Shop != annaBefore.Shop {
		t.Errorf("Home's shop changed by Neighbours: %q", got.Shop)
	}
	if got, _ := f.store.Get(ctx, f.seed.Carl); got.Shop != "Carl's shop" {
		t.Errorf("Neighbours' shop: %q", got.Shop)
	}
	// Signed out: no save at all.
	if rec := f.do(t, req{method: "POST", path: "/profile", form: form, hx: true}); rec.Code == http.StatusOK {
		t.Errorf("signed out: %d", rec.Code)
	}
}

func TestProfileAddYourName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	dana := f.store.AddMember(f.seed.Home, "dana@example.test", "", webfake.SeedPassword, domain.RoleMember)
	danaSession := f.session(dana)

	// Dana sees the field in place of her name; others see a name for her.
	mustContain(t, "dana", f.do(t, req{path: "/profile", session: danaSession}).Body.String(),
		`action="/profile/name"`, `placeholder="Add your name"`)
	annaPage := f.do(t, req{path: "/profile", session: f.session(f.seed.Anna)}).Body.String()
	if strings.Contains(annaPage, `action="/profile/name"`) {
		t.Error("Anna (who has a name) sees Add your name")
	}
	mustContain(t, "anna", annaPage, `<span class="name">dana</span>`)

	// Only the signed-in member is renamed, whatever the form says.
	form := url.Values{"name": {"  Dana  "}, "member_id": {f.seed.Anna.MemberID.String()}}
	rec := f.do(t, req{method: "POST", path: "/profile/name", form: form, session: danaSession, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("name: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "name", rec.Body.String(), `id="household"`, `<span class="name">Dana</span>`)
	names := memberNames(t, f, f.seed.Anna)
	if names["dana@example.test"] != "Dana" || names["anna@example.test"] != "Anna" {
		t.Errorf("names: %v", names)
	}

	if rec := f.do(t, req{method: "POST", path: "/profile/name", form: url.Values{"name": {" "}}, session: danaSession, hx: true}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty name: %d", rec.Code)
	}

	// Carl (another household) can't rename anyone in Home.
	form = url.Values{"name": {"Hacked"}, "member_id": {dana.MemberID.String()}}
	if rec := f.do(t, req{method: "POST", path: "/profile/name", form: form, session: f.session(f.seed.Carl), hx: true}); rec.Code != http.StatusOK {
		t.Fatalf("carl: %d", rec.Code)
	}
	names = memberNames(t, f, f.seed.Anna)
	if names["dana@example.test"] != "Dana" || names["anna@example.test"] != "Anna" {
		t.Errorf("Home's names changed by Neighbours: %v", names)
	}
	// A scope for a household the member isn't in is not found.
	if _, err := f.store.SetMyName(ctx, domain.Scope{HouseholdID: f.seed.Neighbours, MemberID: dana.MemberID}, "X"); err == nil {
		t.Error("SetMyName through another household succeeded")
	}
}

func memberNames(t *testing.T, f *fixture, sc domain.Scope) map[string]string {
	t.Helper()
	ms, err := f.store.Members(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range ms {
		out[m.Email] = m.Name
	}
	return out
}
