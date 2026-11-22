package planner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/planner"
	"github.com/Zagoman/meal-planner/internal/shopping"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

// --- fixtures ---------------------------------------------------------------

func ing(name string, qty float64, unit, section string) domain.IngredientJSON {
	return domain.IngredientJSON{Name: name, NormalizedName: shopping.Normalize(name), Qty: qty, Unit: unit, Section: section}
}

func step(text string) domain.StepJSON { return domain.StepJSON{Text: text} }

func babyStep() domain.StepJSON {
	return domain.StepJSON{Text: "Take out the baby's portion.", BabyPortion: true}
}

func meal(day domain.Weekday, title string, ings []domain.IngredientJSON, steps ...domain.StepJSON) domain.MealJSON {
	if len(steps) == 0 {
		steps = []domain.StepJSON{step("Cook everything together.")}
	}
	return domain.MealJSON{
		Day: day, Title: title, Protein: domain.ProteinChicken, Minutes: 30, OnePot: true, Servings: 2,
		Ingredients: ings, Steps: steps, AfterBabyPortion: []string{},
	}
}

func reply(t *testing.T, meals ...domain.MealJSON) claude.FakeReply {
	t.Helper()
	raw, err := json.Marshal(domain.MealPlanJSON{Meals: meals})
	if err != nil {
		t.Fatal(err)
	}
	return claude.FakeReply{JSON: string(raw)}
}

func weekA(t *testing.T) claude.FakeReply {
	return reply(t,
		meal(domain.Monday, "Chicken traybake", []domain.IngredientJSON{
			ing("onions, sliced", 2, "pcs", "produce"),
			ing("chicken thighs", 500, "g", "meat_fish"),
		}),
		meal(domain.Tuesday, "Onion rice", []domain.IngredientJSON{
			ing("onion", 1, "pcs", "produce"),
			ing("rice", 200, "g", "dry_goods"),
		}),
	)
}

type env struct {
	pool  *pgxpool.Pool
	fake  *claude.Fake
	hub   *events.Hub
	plans *planner.Service
	lists *shopping.Service
	a, b  domain.Scope
}

func newEnv(t *testing.T, replies ...claude.FakeReply) *env {
	t.Helper()
	pool := testutil.NewDB(t)
	e := &env{pool: pool, fake: claude.NewFake(replies...), hub: events.NewHub()}
	e.plans = planner.New(pool, e.fake, e.hub, planner.Options{Timeout: 10 * time.Second})
	e.lists = shopping.New(pool, e.hub)
	e.a = testutil.NewHousehold(t, pool, "A")
	e.b = testutil.NewHousehold(t, pool, "B")
	return e
}

func (e *env) setProfile(t *testing.T, s domain.Scope, f func(*db.UpdateProfileParams)) {
	t.Helper()
	q := db.New(e.pool)
	p, err := q.GetProfile(context.Background(), s.HouseholdID)
	if err != nil {
		t.Fatal(err)
	}
	params := db.UpdateProfileParams{
		HouseholdID: s.HouseholdID, DinnersPerWeek: p.DinnersPerWeek, Servings: p.Servings,
		SpiceLevel: p.SpiceLevel, OnePot: p.OnePot, ProteinMix: p.ProteinMix, Avoid: p.Avoid,
		Shop: p.Shop, ShoppingDay: p.ShoppingDay, BabyPortions: p.BabyPortions, Language: p.Language,
	}
	f(&params)
	if _, err := q.UpdateProfile(context.Background(), params); err != nil {
		t.Fatal(err)
	}
}

func (e *env) usageKinds(t *testing.T, s domain.Scope) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT kind FROM usage_log WHERE household_id = $1 ORDER BY id`, s.HouseholdID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	return kinds
}

func (e *env) countPlans(t *testing.T, s domain.Scope, status string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM plans WHERE household_id = $1 AND status = $2`, s.HouseholdID, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func items(l domain.List) map[string]domain.ListItem {
	out := map[string]domain.ListItem{}
	for _, s := range l.Sections {
		for _, it := range s.Items {
			out[it.Name] = it
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// --- tests ------------------------------------------------------------------

func TestGenerateSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, weekA(t))
	ch, cancel := e.hub.Subscribe(e.a.HouseholdID)
	defer cancel()

	plan, err := e.plans.Generate(ctx, e.a, "quick dinners")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanActive || len(plan.Meals) != 2 || plan.Prompt != "quick dinners" {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.WeekStart.Weekday() != time.Monday {
		t.Errorf("week start %v is not a Monday", plan.WeekStart)
	}
	m := plan.Meals[0]
	if m.Day != domain.Monday || len(m.Ingredients) != 2 || m.Ingredients[0].NormalizedName != "onion" || len(m.Steps) != 1 {
		t.Errorf("monday meal: %+v", m)
	}

	cur, err := e.plans.Current(ctx, e.a)
	if err != nil || cur.ID != plan.ID {
		t.Fatalf("current: %v %v", cur.ID, err)
	}
	got, err := e.plans.Meal(ctx, e.a, m.ID)
	if err != nil || got.Title != "Chicken traybake" {
		t.Fatalf("meal: %+v %v", got, err)
	}

	list, err := e.lists.Active(ctx, e.a)
	if err != nil {
		t.Fatal(err)
	}
	it := items(list)
	if on := it["Onions"]; on.QtyText != "1 net" || on.NeedText != "need 3" || on.Section != "produce" {
		t.Errorf("onions merged wrong: %+v (all: %+v)", on, it)
	}
	if len(it) != 3 {
		t.Errorf("want 3 items, got %+v", it)
	}

	select {
	case ev := <-ch:
		if ev.Kind != events.ListRebuilt {
			t.Errorf("event %+v", ev)
		}
	default:
		t.Error("no ListRebuilt event published")
	}

	req := e.fake.Requests[0]
	if len(req.Schema) == 0 || req.System == "" || len(req.Messages) != 1 {
		t.Fatalf("request: %+v", req)
	}
	if u := req.Messages[0].Content; !strings.Contains(u, "quick dinners") || !strings.Contains(u, "- dry_goods (") {
		t.Errorf("user prompt misses request or section keys:\n%s", u)
	}
	if k := e.usageKinds(t, e.a); !equalStrings(k, []string{"plan"}) {
		t.Errorf("usage kinds %v", k)
	}
}

func TestGenerateRetriesOnceWithProblems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bad := reply(t, meal(domain.Monday, "Mushroom stew", []domain.IngredientJSON{
		ing("chestnut mushrooms", 250, "g", "produce"),
	}))
	e := newEnv(t, bad, weekA(t))
	e.setProfile(t, e.a, func(p *db.UpdateProfileParams) { p.Avoid = []string{"mushrooms"} })

	plan, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Meals) != 2 || e.fake.Calls() != 2 {
		t.Fatalf("meals %d, calls %d", len(plan.Meals), e.fake.Calls())
	}
	retry := e.fake.Requests[1]
	if len(retry.Messages) != 3 || retry.Messages[1].Role != "assistant" || !strings.Contains(retry.Messages[2].Content, "avoid list") {
		t.Fatalf("retry request does not name the problem: %+v", retry.Messages)
	}
	if !strings.Contains(e.fake.Requests[0].Messages[0].Content, `"mushrooms"`) {
		t.Error("avoid list not in the prompt")
	}
	if k := e.usageKinds(t, e.a); !equalStrings(k, []string{"plan", "retry"}) {
		t.Errorf("usage kinds %v", k)
	}
}

func TestGenerateInvalidTwiceKeepsPreviousPlan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	badSection := reply(t, meal(domain.Monday, "Soup", []domain.IngredientJSON{ing("leek", 2, "pcs", "garden_centre")}))
	e := newEnv(t, weekA(t), badSection, badSection)

	first, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.plans.Generate(ctx, e.a, "")
	if !errors.Is(err, domain.ErrGeneration) || !strings.Contains(err.Error(), "unknown section") {
		t.Fatalf("want ErrGeneration naming the problem, got %v", err)
	}
	cur, err := e.plans.Current(ctx, e.a)
	if err != nil || cur.ID != first.ID || len(cur.Meals) != 2 {
		t.Fatalf("previous plan not kept active: %v %v", cur.ID, err)
	}
	if n := e.countPlans(t, e.a, "draft"); n != 0 {
		t.Errorf("%d drafts left behind", n)
	}
	if n := e.countPlans(t, e.a, "archived"); n != 0 {
		t.Errorf("%d plans archived on failure", n)
	}
	if k := e.usageKinds(t, e.a); !equalStrings(k, []string{"plan", "plan", "retry"}) {
		t.Errorf("usage kinds %v", k)
	}
}

func TestGenerateAvoidListTwiceFails(t *testing.T) {
	t.Parallel()
	bad := reply(t, meal(domain.Monday, "Pork stir fry", []domain.IngredientJSON{ing("pork belly", 400, "g", "meat_fish")}))
	e := newEnv(t, bad, bad)
	e.setProfile(t, e.a, func(p *db.UpdateProfileParams) { p.Avoid = []string{"Pork"} })
	_, err := e.plans.Generate(context.Background(), e.a, "")
	if !errors.Is(err, domain.ErrGeneration) || !strings.Contains(err.Error(), "avoid list") {
		t.Fatalf("got %v", err)
	}
	if _, err := e.plans.Current(context.Background(), e.a); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a plan was activated: %v", err)
	}
}

func TestGenerateBabyPortionOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ings := []domain.IngredientJSON{ing("pasta", 300, "g", "dry_goods"), ing("parmesan", 50, "g", "dairy_eggs")}
	wrong := reply(t, meal(domain.Monday, "Pasta", ings,
		step("Boil the pasta."), step("Stir in the parmesan and salt."), babyStep()))
	right := reply(t, meal(domain.Monday, "Pasta", ings,
		step("Boil the pasta."), babyStep(), step("Stir in the parmesan and salt.")))
	e := newEnv(t, wrong, right)
	e.setProfile(t, e.a, func(p *db.UpdateProfileParams) { p.BabyPortions = true })

	plan, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.fake.Requests[1].Messages[2].Content, "before the baby portion") {
		t.Errorf("retry does not name the baby problem: %s", e.fake.Requests[1].Messages[2].Content)
	}
	steps := plan.Meals[0].Steps
	if len(steps) != 3 || !steps[1].BabyPortion || steps[2].BabyPortion {
		t.Fatalf("steps saved wrong: %+v", steps)
	}
	if !strings.Contains(e.fake.Requests[0].System, "Baby portions are ON") {
		t.Error("system prompt lacks baby rules")
	}

	// both answers wrong: no plan
	e2 := newEnv(t, wrong, wrong)
	e2.setProfile(t, e2.a, func(p *db.UpdateProfileParams) { p.BabyPortions = true })
	if _, err := e2.plans.Generate(ctx, e2.a, ""); !errors.Is(err, domain.ErrGeneration) {
		t.Fatalf("got %v", err)
	}
}

func TestGenerateClaudeErrorAndTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, weekA(t), claude.FakeReply{Err: errors.New("overloaded")})
	first, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.plans.Generate(ctx, e.a, ""); !errors.Is(err, domain.ErrGeneration) {
		t.Fatalf("got %v", err)
	}
	if cur, _ := e.plans.Current(ctx, e.a); cur.ID != first.ID {
		t.Error("previous plan lost after a Claude error")
	}

	slow := planner.New(e.pool, blockingClient{}, nil, planner.Options{Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err = slow.Generate(ctx, e.a, "")
	if !errors.Is(err, domain.ErrGeneration) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ErrGeneration + deadline, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout not applied")
	}

	disabled := planner.New(e.pool, nil, nil, planner.Options{})
	if _, err := disabled.Generate(ctx, e.a, ""); !errors.Is(err, domain.ErrGeneration) {
		t.Fatalf("no client: %v", err)
	}
	if _, err := disabled.Current(ctx, e.a); err != nil {
		t.Errorf("reads must work without a client: %v", err)
	}
	if _, err := e.plans.Generate(ctx, e.a, strings.Repeat("x", planner.MaxPromptLen+1)); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("long prompt: %v", err)
	}
}

type blockingClient struct{}

func (blockingClient) Generate(ctx context.Context, _ claude.Request) (claude.Response, error) {
	<-ctx.Done()
	return claude.Response{}, ctx.Err()
}

func TestSwapKeepsTicksAndManualItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newTuesday := reply(t, meal(domain.Tuesday, "Onion pasta", []domain.IngredientJSON{
		ing("onion", 2, "pcs", "produce"),
		ing("pasta", 400, "g", "dry_goods"),
	}))
	e := newEnv(t, weekA(t), newTuesday)
	plan, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := e.lists.Active(ctx, e.a)
	before := items(list)
	for _, name := range []string{"Onions", "Rice"} {
		if _, err := e.lists.SetChecked(ctx, e.a, before[name].ID, true); err != nil {
			t.Fatal(err)
		}
	}
	manual, err := e.lists.AddItem(ctx, e.a, "Coffee", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.lists.SetChecked(ctx, e.a, manual.ID, true); err != nil {
		t.Fatal(err)
	}

	swapped, err := e.plans.SwapMeal(ctx, e.a, plan.ID, domain.Tuesday, "use pasta")
	if err != nil {
		t.Fatal(err)
	}
	if swapped.Title != "Onion pasta" || swapped.Day != domain.Tuesday {
		t.Fatalf("swapped meal %+v", swapped)
	}
	sreq := e.fake.Requests[1].Messages[0].Content
	if !strings.Contains(sreq, `replaces "Onion rice"`) || !strings.Contains(sreq, `"Chicken traybake"`) {
		t.Errorf("swap prompt lacks context:\n%s", sreq)
	}
	if k := e.usageKinds(t, e.a); !equalStrings(k, []string{"plan", "swap"}) {
		t.Errorf("usage kinds %v", k)
	}

	list, _ = e.lists.Active(ctx, e.a)
	after := items(list)
	on := after["Onions"]
	if on.ID != before["Onions"].ID || !on.Checked || on.NeedText != "need 4" || on.CheckedBy == nil || *on.CheckedBy != e.a.MemberID {
		t.Errorf("onion tick lost or not updated: %+v", on)
	}
	if _, ok := after["Rice"]; ok {
		t.Error("rice is still on the list")
	}
	if p := after["Pasta"]; p.Checked || p.QtyText != "1 pack (500 g)" {
		t.Errorf("pasta: %+v", p)
	}
	if c := after["Coffee"]; !c.IsManual || !c.Checked || c.ID != manual.ID {
		t.Errorf("manual item lost: %+v", c)
	}
	cur, _ := e.plans.Current(ctx, e.a)
	if cur.ID != plan.ID || len(cur.Meals) != 2 || cur.Meals[1].Title != "Onion pasta" {
		t.Errorf("plan after swap: %+v", cur.Meals)
	}

	// invalid day and archived plan
	if _, err := e.plans.SwapMeal(ctx, e.a, plan.ID, "someday", ""); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad day: %v", err)
	}
}

func TestSwapFailureKeepsMeal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	wrongDay := reply(t, meal(domain.Friday, "Fish", []domain.IngredientJSON{ing("cod", 400, "g", "meat_fish")}))
	e := newEnv(t, weekA(t), wrongDay, wrongDay)
	plan, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.plans.SwapMeal(ctx, e.a, plan.ID, domain.Tuesday, ""); !errors.Is(err, domain.ErrGeneration) {
		t.Fatalf("got %v", err)
	}
	cur, _ := e.plans.Current(ctx, e.a)
	if cur.Meals[1].Title != "Onion rice" {
		t.Errorf("meal changed after a failed swap: %+v", cur.Meals[1])
	}
}

func TestSubstitute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, weekA(t))
	e.setProfile(t, e.a, func(p *db.UpdateProfileParams) { p.Avoid = []string{"pork"} })
	plan, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	monday := plan.Meals[0]
	got, err := e.plans.Substitute(ctx, e.a, monday.ID, "Chicken thighs", "firm tofu")
	if err != nil {
		t.Fatal(err)
	}
	sub := got.Ingredients[1]
	if sub.Name != "firm tofu" || sub.NormalizedName != "firm tofu" || sub.Qty != 500 || sub.Unit != "g" || sub.Section != "meat_fish" {
		t.Errorf("substituted ingredient %+v", sub)
	}
	list, _ := e.lists.Active(ctx, e.a)
	it := items(list)
	if _, ok := it["Chicken thighs"]; ok {
		t.Error("chicken still on the list")
	}
	if it["Firm tofu"].QtyText != "500 g" {
		t.Errorf("tofu: %+v", it["Firm tofu"])
	}
	for name, args := range map[string][2]string{
		"avoid":   {"onion", "pork mince"},
		"missing": {"saffron", "turmeric"},
		"empty":   {"onion", "  "},
	} {
		if _, err := e.plans.Substitute(ctx, e.a, monday.ID, args[0], args[1]); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
}

// Household B can neither read nor change household A's plan, meals or list.
func TestCrossHouseholdAccessIsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, weekA(t), weekA(t))
	plan, err := e.plans.Generate(ctx, e.a, "")
	if err != nil {
		t.Fatal(err)
	}
	mealA := plan.Meals[0]
	list, _ := e.lists.Active(ctx, e.a)
	itemA := items(list)["Onions"]

	notFound := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want ErrNotFound, got %v", name, err)
		}
	}
	_, err = e.plans.Current(ctx, e.b)
	notFound("Current", err)
	_, err = e.plans.Meal(ctx, e.b, mealA.ID)
	notFound("Meal", err)
	_, err = e.plans.SwapMeal(ctx, e.b, plan.ID, domain.Monday, "")
	notFound("SwapMeal", err)
	_, err = e.plans.Substitute(ctx, e.b, mealA.ID, "onion", "leek")
	notFound("Substitute", err)
	_, err = e.lists.Active(ctx, e.b)
	notFound("Active", err)
	_, err = e.lists.Rebuild(ctx, e.b, plan.ID)
	notFound("Rebuild", err)
	_, err = e.lists.SetChecked(ctx, e.b, itemA.ID, true)
	notFound("SetChecked", err)
	notFound("DeleteItem", e.lists.DeleteItem(ctx, e.b, itemA.ID))
	_, err = e.lists.AddItem(ctx, e.b, "milk", "")
	notFound("AddItem without a plan", err)
	if e.fake.Calls() != 1 {
		t.Errorf("household B triggered Claude calls: %d", e.fake.Calls())
	}

	// B generating its own plan does not touch A's.
	if _, err := e.plans.Generate(ctx, e.b, ""); err != nil {
		t.Fatal(err)
	}
	curA, _ := e.plans.Current(ctx, e.a)
	if curA.ID != plan.ID {
		t.Error("B's generate replaced A's plan")
	}
	list, _ = e.lists.Active(ctx, e.a)
	if a := items(list)["Onions"]; a.Checked || a.ID != itemA.ID {
		t.Errorf("A's item changed: %+v", a)
	}
}
