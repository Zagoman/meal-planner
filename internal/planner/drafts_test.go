package planner_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/planner"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

// draftEnv is env with a fixed clock and a job worker run by hand.
type draftEnv struct {
	*env
	worker *jobs.Worker
	now    time.Time
}

// copenhagen: Friday 2026-11-27 12:00. Households shop on Saturday.
var copenhagen = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		panic(err)
	}
	return loc
}()

func newDraftEnv(t *testing.T, replies ...claude.FakeReply) *draftEnv {
	t.Helper()
	e := newEnv(t, replies...)
	d := &draftEnv{env: e, now: time.Date(2026, 11, 27, 12, 0, 0, 0, copenhagen)}
	d.worker = jobs.NewWorker(e.pool, jobs.Options{})
	e.plans = planner.New(e.pool, e.fake, e.hub, planner.Options{Timeout: 10 * time.Second, Now: func() time.Time { return d.now }})
	e.plans.RegisterJobs(d.worker)
	return d
}

func (d *draftEnv) run(t *testing.T) {
	t.Helper()
	if err := d.worker.RunPending(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// draft makes weekA (two dinners) as the draft for Saturday 2026-11-28.
func (d *draftEnv) draft(t *testing.T) domain.Review {
	t.Helper()
	ctx := context.Background()
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	r, err := d.plans.Review(ctx, d.a)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDraftCardLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t))
	ch, cancel := d.hub.Subscribe(d.a.HouseholdID)
	defer cancel()

	if c, err := d.plans.Card(ctx, d.a); err != nil || c.State != domain.DraftNone {
		t.Fatalf("no draft: %+v %v", c, err)
	}
	job, err := d.plans.RequestDraft(ctx, d.a)
	if err != nil || job.Kind != domain.JobDraft || job.Status != domain.JobQueued {
		t.Fatalf("request: %+v %v", job, err)
	}
	if e := next(t, ch); e.Kind != events.PlanUpdated {
		t.Errorf("event %+v", e)
	}
	// A second tap while it is being made does not queue another job.
	if again, _ := d.plans.RequestDraft(ctx, d.a); again.ID != job.ID {
		t.Error("second draft job queued")
	}
	if c, _ := d.plans.Card(ctx, d.a); c.State != domain.DraftMaking {
		t.Errorf("making: %+v", c)
	}
	d.run(t)
	c, err := d.plans.Card(ctx, d.a)
	if err != nil || c.State != domain.DraftReady || c.MealCount != 2 {
		t.Fatalf("ready: %+v %v", c, err)
	}
	if want := time.Date(2026, 11, 28, 8, 0, 0, 0, copenhagen); !c.ConfirmsAt.Equal(want) {
		t.Errorf("confirms at %v, want %v", c.ConfirmsAt, want)
	}
	r, _ := d.plans.Review(ctx, d.a)
	if r.Plan.Status != domain.PlanDraft || r.Plan.WeekStart.Format(time.DateOnly) != "2026-11-28" {
		t.Errorf("draft plan: %+v", r.Plan)
	}
	// The active plan (none here) and the list are untouched by a draft.
	if _, err := d.lists.Active(ctx, d.a); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a draft made a list: %v", err)
	}
}

func TestDraftFailureKeepsData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bad := claude.FakeReply{JSON: `{"meals": []}`}
	d := newDraftEnv(t, bad, bad)
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	c, err := d.plans.Card(ctx, d.a)
	if err != nil || c.State != domain.DraftFailed || c.Error == "" {
		t.Fatalf("failed card: %+v %v", c, err)
	}
	if n := d.countPlans(t, d.a, "draft"); n != 0 {
		t.Errorf("failed job left %d drafts", n)
	}
}

func TestVotesSwapRemove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	swapped := reply(t, meal(domain.Monday, "Fish pie", []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")}))
	d := newDraftEnv(t, weekA(t), swapped)
	r := d.draft(t)
	monday, tuesday := r.Meals[0], r.Meals[1]
	ch, cancel := d.hub.Subscribe(d.a.HouseholdID)
	defer cancel()

	// Votes: up, down, clear; each publishes the meal.
	for _, v := range []int{1, -1} {
		m, err := d.plans.Vote(ctx, d.a, monday.ID, v)
		if err != nil || m.Mine != v || len(m.Votes) != 1 {
			t.Fatalf("vote %d: %+v %v", v, m, err)
		}
		if e := next(t, ch); e.Kind != events.MealUpdated || e.MealID != monday.ID {
			t.Errorf("event %+v", e)
		}
	}
	if m, _ := d.plans.Vote(ctx, d.a, tuesday.ID, -1); m.Downs() != 1 {
		t.Errorf("downs %+v", m)
	}
	if _, err := d.plans.Vote(ctx, d.a, tuesday.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.Vote(ctx, d.a, tuesday.ID, 2); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("vote 2: %v", err)
	}

	// Swap: the card shows "swapping", then the same meal id has new content
	// and no votes.
	m, err := d.plans.RequestSwap(ctx, d.a, monday.ID)
	if err != nil || !m.Swapping {
		t.Fatalf("swap request: %+v %v", m, err)
	}
	d.run(t)
	m, err = d.plans.DraftMeal(ctx, d.a, monday.ID)
	if err != nil || m.Title != "Fish pie" || m.Swapping || len(m.Votes) != 0 || m.Day != domain.Monday {
		t.Fatalf("swapped: %+v %v", m, err)
	}
	if !strings.Contains(d.fake.Requests[1].Messages[0].Content, `replaces "Chicken traybake"`) {
		t.Errorf("swap prompt: %s", d.fake.Requests[1].Messages[0].Content)
	}

	// Remove.
	if err := d.plans.RemoveMeal(ctx, d.a, tuesday.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := d.plans.Review(ctx, d.a); len(r.Meals) != 1 {
		t.Errorf("after remove: %d meals", len(r.Meals))
	}
	if _, err := d.plans.DraftMeal(ctx, d.a, tuesday.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed meal: %v", err)
	}
}

func TestSwapFailureShowsErrorAndKeepsMeal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bad := claude.FakeReply{Err: errors.New("overloaded")}
	d := newDraftEnv(t, weekA(t), bad)
	r := d.draft(t)
	ch, cancel := d.hub.Subscribe(d.a.HouseholdID)
	defer cancel()
	if _, err := d.plans.RequestSwap(ctx, d.a, r.Meals[0].ID); err != nil {
		t.Fatal(err)
	}
	next(t, ch) // swapping
	d.run(t)
	sawFailed := false
	for range 3 {
		if e := next(t, ch); e.Kind == events.JobFailed && e.MealID == r.Meals[0].ID {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("no job.failed event for the meal")
	}
	m, _ := d.plans.DraftMeal(ctx, d.a, r.Meals[0].ID)
	if m.Title != r.Meals[0].Title || m.Swapping || m.SwapError == "" {
		t.Errorf("after failed swap: %+v", m)
	}
}

func TestAskClaudeDiffsTheDraft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Claude keeps "Onion rice", drops "Chicken traybake", adds two meals.
	asked := reply(t,
		meal(domain.Tuesday, "Onion rice", []domain.IngredientJSON{ing("onion", 1, "pcs", "produce"), ing("rice", 200, "g", "dry_goods")}),
		meal(domain.Monday, "Lentil soup", []domain.IngredientJSON{ing("lentils", 300, "g", "dry_goods")}),
		meal(domain.Friday, "Fish tacos", []domain.IngredientJSON{ing("cod", 300, "g", "meat_fish")}),
	)
	d := newDraftEnv(t, weekA(t), asked)
	r := d.draft(t)
	rice := r.Meals[1]
	if _, err := d.plans.Vote(ctx, d.a, rice.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.RequestAsk(ctx, d.a, "  "); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("empty ask: %v", err)
	}
	if _, err := d.plans.RequestAsk(ctx, d.a, "no chicken this week, add fish tacos"); err != nil {
		t.Fatal(err)
	}
	if r, _ := d.plans.Review(ctx, d.a); !r.Asking {
		t.Error("review does not show the ask running")
	}
	d.run(t)
	r, err := d.plans.Review(ctx, d.a)
	if err != nil || len(r.Meals) != 3 || r.Asking {
		t.Fatalf("after ask: %+v %v", r, err)
	}
	var kept *domain.DraftMeal
	for i := range r.Meals {
		if r.Meals[i].Title == "Chicken traybake" {
			t.Error("removed meal still there")
		}
		if r.Meals[i].ID == rice.ID {
			kept = &r.Meals[i]
		}
	}
	if kept == nil || kept.Mine != 1 {
		t.Errorf("kept meal lost its id or vote: %+v", kept)
	}
	prompt := d.fake.Requests[1].Messages[0].Content
	for _, want := range []string{`dinner, day monday: "Chicken traybake"`, "no chicken this week", "Return the complete set"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("ask prompt lacks %q", want)
		}
	}
}

func TestListPreviewIsNotStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t))
	d.draft(t)
	l, err := d.plans.ListPreview(ctx, d.a)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range l.Sections {
		for _, it := range s.Items {
			names[it.Name] = true
		}
	}
	if !names["Onions"] || !names["Chicken thighs"] || !names["Rice"] {
		t.Errorf("preview %v", names)
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM list_items WHERE household_id = $1`, d.a.HouseholdID).Scan(&n); err != nil || n != 0 {
		t.Errorf("preview stored %d items (%v)", n, err)
	}
}

func TestConfirmOnceAndCarryOver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t), weekA(t))
	// This week's confirmed plan, with a manual item and a tick.
	d.now = time.Date(2026, 11, 21, 12, 0, 0, 0, copenhagen) // Saturday
	first := d.draft(t)
	if _, err := d.plans.Confirm(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	list, _ := d.lists.Active(ctx, d.a)
	onion := byName(list)["Onions"]
	if _, err := d.lists.SetChecked(ctx, d.a, onion.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.lists.AddItem(ctx, d.a, "Coffee", "other"); err != nil {
		t.Fatal(err)
	}
	// Same day: a new draft goes to next week (this week is confirmed) and
	// confirming it starts a fresh list.
	if _, err := d.plans.RequestDraft(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	r, _ := d.plans.Review(ctx, d.a)
	if r.Plan.WeekStart.Format(time.DateOnly) != "2026-11-28" {
		t.Fatalf("second draft week %v", r.Plan.WeekStart)
	}

	// Two members confirm at once: one confirmation, no error.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = d.plans.Confirm(ctx, d.a)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("confirm: %v", errs)
	}
	if n := d.countPlans(t, d.a, "active"); n != 1 {
		t.Errorf("%d active plans", n)
	}
	if n := d.countPlans(t, d.a, "archived"); n != 1 {
		t.Errorf("%d archived plans", n)
	}
	cur, _ := d.plans.Current(ctx, d.a)
	if cur.ID != r.Plan.ID || cur.ConfirmedAt == nil || cur.AutoConfirmed {
		t.Errorf("current %+v", cur)
	}
	list, _ = d.lists.Active(ctx, d.a)
	if it := byName(list)["Onions"]; it.Checked {
		t.Error("next week's list kept last week's tick")
	}
	if _, ok := byName(list)["Coffee"]; ok {
		t.Error("next week's list kept last week's manual item")
	}
	_ = first
}

func TestConfirmSameWeekKeepsManualItemsAndTicks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t), weekA(t))
	d.now = time.Date(2026, 11, 28, 7, 0, 0, 0, copenhagen) // Saturday, shopping day
	d.draft(t)
	if _, err := d.plans.Confirm(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	list, _ := d.lists.Active(ctx, d.a)
	if _, err := d.lists.SetChecked(ctx, d.a, byName(list)["Onions"].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.lists.AddItem(ctx, d.a, "Coffee", "other"); err != nil {
		t.Fatal(err)
	}
	// A draft for the same week (forced) replaces the list but keeps them.
	if _, err := d.pool.Exec(ctx, `UPDATE plans SET week_start = '2026-11-28' WHERE household_id = $1`, d.a.HouseholdID); err != nil {
		t.Fatal(err)
	}
	q := db.New(d.pool)
	if _, err := jobs.Enqueue(ctx, q, jobs.New{HouseholdID: d.a.HouseholdID, Kind: domain.JobDraft,
		Payload: map[string]any{"week_start": "2026-11-28", "replace": true}}); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if _, err := d.plans.Confirm(ctx, d.a); err != nil {
		t.Fatal(err)
	}
	list, _ = d.lists.Active(ctx, d.a)
	items := byName(list)
	if !items["Onions"].Checked {
		t.Error("tick on an item still needed was lost")
	}
	if _, ok := items["Coffee"]; !ok {
		t.Error("manual item was lost")
	}
}

func TestScheduleDraftAndAutoConfirm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Weekly draft, then at auto-confirm the disliked dinner is swapped in
	// one call and the plan confirms itself.
	replacement := reply(t, meal(domain.Monday, "Veggie chili", []domain.IngredientJSON{ing("beans", 400, "g", "tins_jars")}))
	d := newDraftEnv(t, weekA(t), replacement)
	d.setProfile(t, d.b, func(p *db.UpdateProfileParams) { p.ShoppingDay = "monday" })

	d.now = time.Date(2026, 11, 27, 17, 59, 0, 0, copenhagen) // Friday before 18:00
	if err := d.plans.Tick(ctx, d.now); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if c, _ := d.plans.Card(ctx, d.a); c.State != domain.DraftNone {
		t.Fatalf("draft before 18:00: %+v", c)
	}
	d.now = time.Date(2026, 11, 27, 18, 0, 0, 0, copenhagen)
	for range 3 { // every minute; fires once
		if err := d.plans.Tick(ctx, d.now); err != nil {
			t.Fatal(err)
		}
		d.run(t)
	}
	r, err := d.plans.Review(ctx, d.a)
	if err != nil || len(r.Meals) != 2 {
		t.Fatalf("weekly draft: %+v %v", r, err)
	}
	if d.fake.Calls() != 1 {
		t.Errorf("%d Claude calls for one weekly draft", d.fake.Calls())
	}
	if _, err := d.plans.Vote(ctx, d.a, r.Meals[0].ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plans.Vote(ctx, d.a, r.Meals[1].ID, 1); err != nil {
		t.Fatal(err)
	}

	d.now = time.Date(2026, 11, 28, 7, 59, 0, 0, copenhagen)
	if err := d.plans.Tick(ctx, d.now); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	if d.countPlans(t, d.a, "draft") != 1 {
		t.Fatal("confirmed before 08:00")
	}
	d.now = time.Date(2026, 11, 28, 8, 0, 0, 0, copenhagen)
	for range 2 {
		if err := d.plans.Tick(ctx, d.now); err != nil {
			t.Fatal(err)
		}
		d.run(t)
	}
	cur, err := d.plans.Current(ctx, d.a)
	if err != nil || !cur.AutoConfirmed || cur.ConfirmNote != "" {
		t.Fatalf("auto-confirmed: %+v %v", cur, err)
	}
	titles := []string{cur.Meals[0].Title, cur.Meals[1].Title}
	if titles[0] != "Veggie chili" || titles[1] != "Onion rice" {
		t.Errorf("meals after auto-confirm: %v", titles)
	}
	if !strings.Contains(d.fake.Requests[1].Messages[0].Content, `"Chicken traybake"`) {
		t.Error("replace prompt does not name the disliked meal")
	}
	if kinds := d.usageKinds(t, d.a); len(kinds) != 2 || kinds[1] != "auto_confirm" {
		t.Errorf("usage kinds %v", kinds)
	}
	if _, err := d.lists.Active(ctx, d.a); err != nil {
		t.Errorf("no list after auto-confirm: %v", err)
	}
}

func TestAutoConfirmKeepsDislikedWhenClaudeFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t), claude.FakeReply{Err: errors.New("overloaded")})
	r := d.draft(t)
	if _, err := d.plans.Vote(ctx, d.a, r.Meals[0].ID, -1); err != nil {
		t.Fatal(err)
	}
	d.now = time.Date(2026, 11, 28, 9, 0, 0, 0, copenhagen)
	if err := d.plans.Tick(ctx, d.now); err != nil {
		t.Fatal(err)
	}
	d.run(t)
	cur, err := d.plans.Current(ctx, d.a)
	if err != nil || !cur.AutoConfirmed || !strings.Contains(cur.ConfirmNote, "Couldn't swap 1 disliked meal") {
		t.Fatalf("auto-confirm with failed swap: %+v %v", cur, err)
	}
	if cur.Meals[0].Title != "Chicken traybake" {
		t.Errorf("disliked meal not kept: %s", cur.Meals[0].Title)
	}
}

// Household B can neither see nor change A's draft.
func TestDraftsCrossHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDraftEnv(t, weekA(t))
	r := d.draft(t)
	mealID := r.Meals[0].ID
	notFound := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want not found, got %v", name, err)
		}
	}
	_, err := d.plans.Review(ctx, d.b)
	notFound("Review", err)
	_, err = d.plans.DraftMeal(ctx, d.b, mealID)
	notFound("DraftMeal", err)
	_, err = d.plans.Vote(ctx, d.b, mealID, -1)
	notFound("Vote", err)
	_, err = d.plans.RequestSwap(ctx, d.b, mealID)
	notFound("RequestSwap", err)
	notFound("RemoveMeal", d.plans.RemoveMeal(ctx, d.b, mealID))
	_, err = d.plans.RequestAsk(ctx, d.b, "more fish")
	notFound("RequestAsk", err)
	_, err = d.plans.ListPreview(ctx, d.b)
	notFound("ListPreview", err)
	_, err = d.plans.Confirm(ctx, d.b)
	notFound("Confirm", err)
	if c, _ := d.plans.Card(ctx, d.b); c.State != domain.DraftNone {
		t.Errorf("B card %+v", c)
	}
	// A's draft is unchanged.
	if r2, _ := d.plans.Review(ctx, d.a); len(r2.Meals) != 2 || len(r2.Meals[0].Votes) != 0 || r2.Plan.Status != domain.PlanDraft {
		t.Errorf("A's draft changed: %+v", r2)
	}
	_ = uuid.Nil
	_ = shopping.Normalize
}

func next(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return events.Event{}
	}
}

func byName(l domain.List) map[string]domain.ListItem {
	out := map[string]domain.ListItem{}
	for _, s := range l.Sections {
		for _, it := range s.Items {
			out[it.Name] = it
		}
	}
	return out
}
