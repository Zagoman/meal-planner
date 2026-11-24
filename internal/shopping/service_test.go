package shopping_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/shopping"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

// activePlan stores an active plan with one meal and the given ingredients.
func activePlan(t *testing.T, pool *pgxpool.Pool, s domain.Scope, ings ...domain.Ingredient) (planID, mealID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)
	p, err := q.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: s.HouseholdID, WeekStart: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.ActivatePlan(ctx, db.ActivatePlanParams{ID: p.ID, HouseholdID: s.HouseholdID}); err != nil {
		t.Fatal(err)
	}
	m, err := q.CreateMeal(ctx, db.CreateMealParams{
		HouseholdID: s.HouseholdID, PlanID: p.ID, Day: pgtype.Text{String: "monday", Valid: true}, MealType: "dinner",
		Title: "Stew", Protein: "other", Minutes: 30, OnePot: true, Servings: 2, AfterBabyPortion: []string{},
		AddedFrom: "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range ings {
		if err := q.CreateIngredient(ctx, db.CreateIngredientParams{
			HouseholdID: s.HouseholdID, MealID: m.ID, Position: int32(i), Name: in.Name, //nolint:gosec // tiny
			NormalizedName: shopping.Normalize(in.Name), Qty: in.Qty, Unit: in.Unit, Section: in.Section,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return p.ID, m.ID
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

func next(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	return events.Event{}
}

func TestListLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	hub := events.NewHub()
	svc := shopping.New(pool, hub)
	a := testutil.NewHousehold(t, pool, "A")
	ch, cancel := hub.Subscribe(a.HouseholdID)
	defer cancel()

	if _, err := svc.Active(ctx, a); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no plan: %v", err)
	}
	planID, _ := activePlan(t, pool, a,
		domain.Ingredient{Name: "carrots", Qty: 400, Unit: "g", Section: "produce"},
		domain.Ingredient{Name: "salt", Qty: 0, Unit: "", Section: "spices"},
		domain.Ingredient{Name: "cod", Qty: 300, Unit: "g", Section: "meat_fish"},
		domain.Ingredient{Name: "cold water", Qty: 500, Unit: "ml", Section: "other"},
	)
	q := db.New(pool)
	if err := q.InsertPantryItem(ctx, db.InsertPantryItemParams{HouseholdID: a.HouseholdID, Name: "Salt", NormalizedName: "salt"}); err != nil {
		t.Fatal(err)
	}

	list, err := svc.Rebuild(ctx, a, planID)
	if err != nil {
		t.Fatal(err)
	}
	if e := next(t, ch); e.Kind != events.ListRebuilt {
		t.Errorf("event %+v", e)
	}
	// Salt is under Check at home only; water is never listed.
	if list.PlanID != planID || len(list.Sections) != 2 || len(list.CheckHome) != 1 {
		t.Fatalf("list %+v", list)
	}
	salt := list.CheckHome[0]
	if salt.Name != "Salt" || !salt.CheckHome || salt.HaveIt != nil || salt.Section != "spices" {
		t.Errorf("check at home: %+v", salt)
	}
	if done, total := list.Progress(); done != 0 || total != 2 {
		t.Errorf("progress %d/%d counts pantry items", done, total)
	}

	// Buy moves salt into its section; Have it hides it again.
	yes, no := true, false
	if _, err := svc.SetHaveIt(ctx, a, salt.ID, &no); err != nil {
		t.Fatal(err)
	}
	next(t, ch)
	list, _ = svc.Active(ctx, a)
	if _, ok := byName(list)["Salt"]; !ok || len(list.CheckHome) != 1 || *list.CheckHome[0].HaveIt {
		t.Errorf("after Buy: %+v", list)
	}
	if _, total := list.Progress(); total != 3 {
		t.Errorf("Buy item not counted: %d", total)
	}
	if _, err := svc.SetHaveIt(ctx, a, salt.ID, &yes); err != nil {
		t.Fatal(err)
	}
	next(t, ch)
	list, _ = svc.Active(ctx, a)
	if _, ok := byName(list)["Salt"]; ok {
		t.Errorf("Have it item still in a section: %+v", list)
	}
	// An answer survives a rebuild.
	if list, err = svc.Rebuild(ctx, a, planID); err != nil || list.CheckHome[0].HaveIt == nil || !*list.CheckHome[0].HaveIt {
		t.Errorf("answer lost on rebuild: %+v %v", list.CheckHome, err)
	}
	next(t, ch)
	items := byName(list)
	if _, err := svc.SetHaveIt(ctx, a, items["Cod"].ID, &yes); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Have it on a normal item: %v", err)
	}
	if c := items["Carrots"]; c.QtyText != "1 bag (1 kg)" || c.NeedText != "need 400 g" {
		t.Errorf("carrots %+v", c)
	}

	// tick, add manual, rebuild: tick and manual item stay
	ticked, err := svc.SetChecked(ctx, a, items["Carrots"].ID, true)
	if err != nil || !ticked.Checked || ticked.CheckedBy == nil || *ticked.CheckedBy != a.MemberID {
		t.Fatalf("tick: %+v %v", ticked, err)
	}
	if e := next(t, ch); e.Kind != events.ItemChanged || e.ItemID != ticked.ID {
		t.Errorf("event %+v", e)
	}
	manual, err := svc.AddItem(ctx, a, "  Dish soap ", "")
	if err != nil {
		t.Fatal(err)
	}
	if manual.Name != "Dish soap" || manual.Section != "other" || !manual.IsManual || manual.PlanID != planID {
		t.Errorf("manual %+v", manual)
	}
	next(t, ch)

	// a meal ingredient goes away: its generated item is removed on rebuild
	if _, err := pool.Exec(ctx, `DELETE FROM ingredients WHERE household_id = $1 AND name = 'cod'`, a.HouseholdID); err != nil {
		t.Fatal(err)
	}
	list, err = svc.Rebuild(ctx, a, planID)
	if err != nil {
		t.Fatal(err)
	}
	next(t, ch)
	items = byName(list)
	if _, ok := items["Cod"]; ok {
		t.Error("stale item kept")
	}
	if !items["Carrots"].Checked || items["Carrots"].ID != ticked.ID {
		t.Error("tick lost on rebuild")
	}
	if items["Dish soap"].ID != manual.ID {
		t.Error("manual item lost on rebuild")
	}

	// untick clears checked_by
	unticked, err := svc.SetChecked(ctx, a, ticked.ID, false)
	if err != nil || unticked.Checked || unticked.CheckedBy != nil {
		t.Errorf("untick: %+v %v", unticked, err)
	}
	next(t, ch)

	if err := svc.DeleteItem(ctx, a, manual.ID); err != nil {
		t.Fatal(err)
	}
	if e := next(t, ch); e.Kind != events.ItemDeleted || e.ItemID != manual.ID {
		t.Errorf("event %+v", e)
	}
	if err := svc.DeleteItem(ctx, a, manual.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("double delete: %v", err)
	}
	if _, err := svc.SetChecked(ctx, a, uuid.New(), true); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
	if _, err := svc.Rebuild(ctx, a, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown plan: %v", err)
	}
}

func TestAddItemValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	svc := shopping.New(pool, nil)
	a := testutil.NewHousehold(t, pool, "A")
	activePlan(t, pool, a)

	for name, args := range map[string][2]string{
		"empty name":      {"   ", "produce"},
		"unknown section": {"milk", "garden"},
		"punctuation":     {"!!!", "produce"},
	} {
		if _, err := svc.AddItem(ctx, a, args[0], args[1]); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s: want validation error, got %v", name, err)
		}
	}
	if _, err := svc.AddItem(ctx, a, "Olive oil", domain.PantryCheckSection); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("pantry_check is not a section to add to: %v", err)
	}
}

// Household B cannot read or change household A's list.
func TestListCrossHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	svc := shopping.New(pool, nil)
	a := testutil.NewHousehold(t, pool, "A")
	b := testutil.NewHousehold(t, pool, "B")
	planA, _ := activePlan(t, pool, a, domain.Ingredient{Name: "milk", Qty: 1, Unit: "l", Section: "dairy_eggs"})
	listA, err := svc.Rebuild(ctx, a, planA)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(listA)["Milk"]
	planB, _ := activePlan(t, pool, b)

	if _, err := svc.Rebuild(ctx, b, planA); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Rebuild: %v", err)
	}
	if _, err := svc.SetChecked(ctx, b, item.ID, true); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetChecked: %v", err)
	}
	if err := svc.DeleteItem(ctx, b, item.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteItem: %v", err)
	}
	have := true
	if _, err := svc.SetHaveIt(ctx, b, item.ID, &have); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetHaveIt: %v", err)
	}
	listB, err := svc.Active(ctx, b)
	if err != nil || listB.PlanID != planB || len(listB.Sections) != 0 {
		t.Errorf("B sees %+v %v", listB, err)
	}
	if _, err := svc.AddItem(ctx, b, "bread", "bakery"); err != nil {
		t.Fatal(err)
	}
	listA, _ = svc.Active(ctx, a)
	if got := byName(listA); len(got) != 1 || got["Milk"].Checked {
		t.Errorf("A's list changed: %+v", got)
	}
}
