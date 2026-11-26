package planner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

// confirmed makes weekA the active plan through the review flow.
func (d *draftEnv) confirmed(t *testing.T) domain.Plan {
	t.Helper()
	d.draft(t)
	p, err := d.plans.Confirm(context.Background(), d.a)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func note(s string) *string { return &s }

func TestRateAFullWeek(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t), weekA(t))
	plan := d.confirmed(t)
	l, err := d.plans.RateList(ctx, d.a)
	if err != nil || len(l.Meals) != 2 || l.Done() != 0 {
		t.Fatalf("rate list: %+v %v", l, err)
	}
	// A note needs a rating or "not cooked" first.
	if _, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[0].ID, domain.OutcomeInput{Note: note("hm")}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("note first: %v", err)
	}
	m, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[0].ID, domain.OutcomeInput{Rating: 4})
	if err != nil || m.Outcome == nil || m.Outcome.Status != domain.Cooked || m.Outcome.Rating != 4 {
		t.Fatalf("rate: %+v %v", m, err)
	}
	if m, _ = d.plans.SetOutcome(ctx, d.a, plan.Meals[0].ID, domain.OutcomeInput{Note: note(" baby loved it ")}); m.Outcome.Rating != 4 || m.Outcome.Note != "baby loved it" {
		t.Errorf("note kept rating: %+v", m.Outcome)
	}
	if m, _ = d.plans.SetOutcome(ctx, d.a, plan.Meals[1].ID, domain.OutcomeInput{NotCooked: true}); m.Outcome.Status != domain.NotCooked || m.Outcome.Rating != 0 {
		t.Errorf("not cooked: %+v", m.Outcome)
	}
	if _, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[1].ID, domain.OutcomeInput{Rating: 6}); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("rating 6: %v", err)
	}
	if l, _ = d.plans.RateList(ctx, d.a); l.Done() != 2 {
		t.Errorf("done %d", l.Done())
	}
	// Clearing takes the answer back.
	if m, _ = d.plans.SetOutcome(ctx, d.a, plan.Meals[1].ID, domain.OutcomeInput{Clear: true}); m.Outcome != nil {
		t.Errorf("cleared: %+v", m.Outcome)
	}
	// Ratings feed the next prompt.
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if prompt := d.fake.Requests[1].Messages[0].Content; !strings.Contains(prompt, `"Chicken traybake" (chicken): 4/5 - "baby loved it"`) {
		t.Errorf("rating not in prompt:\n%s", prompt)
	}
	// The card opens the rating step while a meal is unanswered.
	if c, _ := d.plans.Card(ctx, d.a); c.State != domain.DraftReady || !c.RateFirst {
		t.Errorf("card %+v", c)
	}
}

func TestNotCookedComesBackOnlyWhenAdded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// The second draft first suggests the not-cooked meal again (rejected),
	// then a valid week.
	again := reply(t,
		meal(domain.Monday, "Onion rice", []domain.IngredientJSON{ing("onion", 1, "pcs", "produce"), ing("rice", 200, "g", "dry_goods")}),
		meal(domain.Tuesday, "Fish pie", []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")}),
	)
	fresh := reply(t,
		meal(domain.Monday, "Lentil soup", []domain.IngredientJSON{ing("lentils", 300, "g", "dry_goods")}),
		meal(domain.Tuesday, "Fish pie", []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")}),
	)
	d := newDraftEnv(t, weekA(t), again, fresh)
	plan := d.confirmed(t)
	rice := plan.Meals[1]
	if _, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[0].ID, domain.OutcomeInput{Rating: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.SetOutcome(ctx, d.a, rice.ID, domain.OutcomeInput{NotCooked: true}); err != nil {
		t.Fatal(err)
	}
	r := d.draft(t)
	if !strings.Contains(d.fake.Requests[1].Messages[0].Content, `"Onion rice" (not cooked last week)`) {
		t.Errorf("prompt does not name the not-cooked meal")
	}
	if retry := d.fake.Requests[2].Messages[2].Content; !strings.Contains(retry, `does not want "Onion rice" (not cooked last week)`) {
		t.Errorf("retry: %s", retry)
	}
	if len(r.NotCooked) != 1 || r.NotCooked[0].Title != "Onion rice" {
		t.Fatalf("not cooked list: %+v", r.NotCooked)
	}
	for _, m := range r.Meals {
		if m.Title == "Onion rice" {
			t.Error("not-cooked meal in the new draft")
		}
	}
	dm, err := d.plans.AddToNextWeek(ctx, d.a, r.NotCooked[0].RecipeID, domain.FromNotCooked)
	if err != nil || dm.Title != "Onion rice" || dm.AddedFrom != domain.FromNotCooked || len(dm.Ingredients) != 2 {
		t.Fatalf("add back: %+v %v", dm, err)
	}
	r, _ = d.plans.Review(ctx, d.a)
	if len(r.Meals) != 3 || len(r.NotCooked) != 0 {
		t.Errorf("after add back: %d meals, not cooked %+v", len(r.Meals), r.NotCooked)
	}
}

func TestRatedOneIsNeverSuggested(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t), weekA(t), weekA(t))
	plan := d.confirmed(t)
	if _, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[0].ID, domain.OutcomeInput{Rating: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if c, _ := d.plans.Card(ctx, d.a); c.State != domain.DraftFailed {
		t.Errorf("a week with the rated-1 meal was accepted: %+v", c)
	}
	if !strings.Contains(d.fake.Requests[1].Messages[0].Content, `"Chicken traybake" (rated 1)`) {
		t.Error("prompt does not name the rated-1 meal")
	}
}

func TestLibrary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tweaked := reply(t, meal(domain.Monday, "Chicken traybake with lemon", []domain.IngredientJSON{
		ing("chicken thighs", 500, "g", "meat_fish"), ing("lemon", 1, "pcs", "produce"), ing("water", 2, "dl", "other"),
	}))
	d := newDraftEnv(t, weekA(t), weekA(t), tweaked)
	plan := d.confirmed(t)
	if _, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[0].ID, domain.OutcomeInput{Rating: 5, Note: note("again!")}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.SetOutcome(ctx, d.a, plan.Meals[1].ID, domain.OutcomeInput{Rating: 3}); err != nil {
		t.Fatal(err)
	}
	// Both cooked meals are in the library; a meal suggested again reuses it.
	d.draft(t)
	all, err := d.plans.Search(ctx, d.a, "", domain.FilterAll)
	if err != nil || len(all) != 2 {
		t.Fatalf("library: %+v %v", all, err)
	}
	if all[0].Title != "Chicken traybake" || all[0].TimesCooked != 1 || all[0].Rating != 5 || all[0].Source != domain.SourceClaude {
		t.Errorf("first: %+v", all[0])
	}
	check := func(q string, f domain.RecipeFilter, want int) {
		t.Helper()
		got, err := d.plans.Search(ctx, d.a, q, f)
		if err != nil || len(got) != want {
			t.Errorf("search %q %q: %d %v", q, f, len(got), err)
		}
	}
	check("TRAY", domain.FilterAll, 1)
	check("rice", domain.FilterAll, 1) // an ingredient of "Onion rice"
	check("%", domain.FilterAll, 0)
	check("", domain.FilterRated4, 1)
	check("", domain.FilterOwn, 0)
	check("", "dinner", 2)
	check("", "baking", 0)
	if _, err := d.plans.Search(ctx, d.a, "", "dessert"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad filter: %v", err)
	}

	// Tweak: a new version, the original stays.
	orig := all[0].ID
	if _, err := d.plans.RequestTweak(ctx, d.a, orig, "add lemon"); err != nil {
		t.Fatal(err)
	}
	if det, _ := d.plans.RecipeDetail(ctx, d.a, orig); det.Tweak == nil || !det.Tweak.Status.Pending() {
		t.Errorf("tweak pending: %+v", det.Tweak)
	}
	d.run(t)
	det, err := d.plans.RecipeDetail(ctx, d.a, orig)
	if err != nil || det.TweakedID == nil || det.LatestNote != "again!" || det.TimesCooked != 1 {
		t.Fatalf("after tweak: %+v %v", det, err)
	}
	v2, err := d.plans.RecipeDetail(ctx, d.a, *det.TweakedID)
	if err != nil || v2.Meal.Title != "Chicken traybake with lemon" || v2.ParentID == nil || *v2.ParentID != orig {
		t.Fatalf("tweaked recipe: %+v %v", v2, err)
	}
	if !strings.Contains(d.fake.Requests[2].Messages[0].Content, `"title":"Chicken traybake"`) {
		t.Error("tweak prompt lacks the recipe")
	}

	// Add the tweak to this week: only missing ingredients, never water.
	before, _ := d.lists.Active(ctx, d.a)
	n, err := d.plans.AddToThisWeek(ctx, d.a, v2.ID)
	if err != nil || n != 1 {
		t.Fatalf("add to this week: %d %v", n, err)
	}
	after, _ := d.lists.Active(ctx, d.a)
	items := byName(after)
	if it, ok := items["Lemon"]; !ok || it.Checked || !it.IsManual || it.QtyText != "1" {
		t.Errorf("lemon: %+v", it)
	}
	if len(items) != len(byName(before))+1 {
		t.Errorf("items %d -> %d", len(byName(before)), len(items))
	}
	if n, _ := d.plans.AddToThisWeek(ctx, d.a, v2.ID); n != 0 {
		t.Errorf("second add: %d", n)
	}
}

func TestAddToNextWeekWithoutDraft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t), weekA(t), weekA(t))
	plan := d.confirmed(t)
	rice := *plan.Meals[1].RecipeID // a planned recipe, not in the library: still addable
	// Saturday: this week is confirmed, so the empty draft is for next week.
	dm, err := d.plans.AddToNextWeek(ctx, d.a, rice, domain.FromLibrary)
	if err != nil || dm.AddedFrom != domain.FromLibrary {
		t.Fatalf("add: %+v %v", dm, err)
	}
	r, _ := d.plans.Review(ctx, d.a)
	if len(r.Meals) != 1 || r.Plan.WeekStart.Format(time.DateOnly) != "2026-12-05" {
		t.Fatalf("empty draft: %d meals, week %v", len(r.Meals), r.Plan.WeekStart)
	}
	// Making the plan fills the draft and keeps the meal.
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	r, _ = d.plans.Review(ctx, d.a)
	if len(r.Meals) != 3 {
		t.Fatalf("filled draft: %d meals", len(r.Meals))
	}
	kept := 0
	for _, m := range r.Meals {
		if m.AddedFrom == domain.FromLibrary && m.Title == "Onion rice" {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("hand-added meal kept %d times", kept)
	}
	if !strings.Contains(d.fake.Requests[1].Messages[0].Content, `already added "Onion rice"`) {
		t.Error("prompt does not mention the added meal")
	}
}

func TestLibraryCrossHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t))
	plan := d.confirmed(t)
	id := *plan.Meals[0].RecipeID
	nf := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want not found, got %v", name, err)
		}
	}
	if got, _ := d.plans.Search(ctx, d.b, "", domain.FilterAll); len(got) != 0 {
		t.Errorf("B sees A's library: %+v", got)
	}
	_, err := d.plans.RecipeDetail(ctx, d.b, id)
	nf("RecipeDetail", err)
	_, err = d.plans.RequestTweak(ctx, d.b, id, "more salt")
	nf("RequestTweak", err)
	_, err = d.plans.AddToNextWeek(ctx, d.b, id, domain.FromLibrary)
	nf("AddToNextWeek", err)
	_, err = d.plans.AddToThisWeek(ctx, d.b, id)
	nf("AddToThisWeek", err)
	_, err = d.plans.SetOutcome(ctx, d.b, plan.Meals[0].ID, domain.OutcomeInput{Rating: 1})
	nf("SetOutcome", err)
	_, err = d.plans.RateList(ctx, d.b)
	nf("RateList", err)
	if l, _ := d.plans.RateList(ctx, d.a); l.Done() != 0 {
		t.Error("B rated A's meal")
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM recipes WHERE household_id = $1`, d.b.HouseholdID).Scan(&n); err != nil || n != 0 {
		t.Errorf("B has %d recipes (%v)", n, err)
	}
	_ = db.New
	_ = claude.FakeReply{}
}
