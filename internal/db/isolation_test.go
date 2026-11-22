package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lucaszago/meal-planner/internal/db"
	"github.com/lucaszago/meal-planner/internal/testutil"
)

// The schema itself refuses rows that point at another household's parent,
// even if a service forgets a filter.
func TestCompositeKeysBlockCrossHouseholdRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	a := testutil.NewHousehold(t, pool, "A")
	b := testutil.NewHousehold(t, pool, "B")

	planA, err := q.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: a.HouseholdID, WeekStart: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	_, err = q.CreateMeal(ctx, db.CreateMealParams{
		HouseholdID: b.HouseholdID, PlanID: planA.ID, Day: "monday",
		Title: "intruder", Protein: "fish", Minutes: 10, OnePot: true, Servings: 2,
	})
	if err == nil {
		t.Fatal("household B attached a meal to household A's plan")
	}

	if _, err := q.AddManualListItem(ctx, db.AddManualListItemParams{
		HouseholdID: b.HouseholdID, PlanID: planA.ID, Name: "x", NormalizedName: "x", Section: "other",
	}); err == nil {
		t.Fatal("household B added a list item to household A's plan")
	}
}

func TestScopedQueriesHideOtherHouseholds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	a := testutil.NewHousehold(t, pool, "A")
	b := testutil.NewHousehold(t, pool, "B")

	planA, err := q.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: a.HouseholdID, WeekStart: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetPlan(ctx, db.GetPlanParams{ID: planA.ID, HouseholdID: b.HouseholdID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("household B read A's plan: err=%v", err)
	}
	if n, err := q.ActivatePlan(ctx, db.ActivatePlanParams{ID: planA.ID, HouseholdID: b.HouseholdID}); err != nil || n != 0 {
		t.Fatalf("household B activated A's plan: rows=%d err=%v", n, err)
	}
}

func TestOnlyOneActivePlanPerHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	a := testutil.NewHousehold(t, pool, "A")

	for i := 0; i < 2; i++ {
		p, err := q.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: a.HouseholdID, WeekStart: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		_, err = q.ActivatePlan(ctx, db.ActivatePlanParams{ID: p.ID, HouseholdID: a.HouseholdID})
		if i == 1 && err == nil {
			t.Fatal("a second plan became active without archiving the first")
		}
	}
}
