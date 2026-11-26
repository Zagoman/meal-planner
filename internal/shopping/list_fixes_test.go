package shopping_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/shopping"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

// Shopping list fixes #1 and #6: ticked items sink to the end of their
// section; "Clear ticked" hides exactly the ticked items to buy, a rebuild
// keeps them hidden, Undo brings that batch back; another household can't
// clear or undo.
func TestTickSinksClearAndUndo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	svc := shopping.New(pool, nil)
	a := testutil.NewHousehold(t, pool, "A")
	b := testutil.NewHousehold(t, pool, "B")
	planID, _ := activePlan(t, pool, a,
		domain.Ingredient{Name: "apples", Qty: 4, Unit: "pcs", Section: "produce"},
		domain.Ingredient{Name: "dill", Qty: 1, Unit: "bunch", Section: "produce"},
		domain.Ingredient{Name: "leeks", Qty: 2, Unit: "pcs", Section: "produce"},
	)
	list, err := svc.Rebuild(ctx, a, planID)
	if err != nil {
		t.Fatal(err)
	}
	items := byName(list)
	if _, err := svc.SetChecked(ctx, a, items["Apple"].ID, true); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.Active(ctx, a)
	var order []string
	for _, it := range list.Sections[0].Items {
		order = append(order, it.Name)
	}
	if len(order) != 3 || order[2] != "Apple" || order[0] != "Dill" {
		t.Errorf("ticked item didn't sink: %v", order)
	}

	// Another household: nothing to clear (its own empty plan), can't undo A's batch.
	if _, _, err := svc.ClearChecked(ctx, b); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("B clear without a plan: %v", err)
	}
	batch, n, err := svc.ClearChecked(ctx, a)
	if err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if _, err := svc.UndoClear(ctx, b, batch); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("B undid A's clear: %v", err)
	}
	list, _ = svc.Active(ctx, a)
	if _, ok := byName(list)["Apple"]; ok || len(byName(list)) != 2 {
		t.Errorf("after clear: %v", byName(list))
	}
	if done, total := list.Progress(); done != 0 || total != 2 {
		t.Errorf("progress after clear: %d of %d", done, total)
	}
	// A rebuild (a meal swapped) keeps bought items off the list.
	if list, _ = svc.Rebuild(ctx, a, planID); len(byName(list)) != 2 {
		t.Errorf("rebuild brought cleared items back: %v", byName(list))
	}
	if n, err := svc.UndoClear(ctx, a, batch); err != nil || n != 1 {
		t.Fatalf("undo: %d %v", n, err)
	}
	list, _ = svc.Active(ctx, a)
	if it := byName(list)["Apple"]; !it.Checked {
		t.Errorf("undo didn't bring back the ticked item: %+v", it)
	}
	if _, err := svc.UndoClear(ctx, a, batch); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second undo: %v", err)
	}
	// Nothing ticked: nothing cleared.
	if _, err := svc.SetChecked(ctx, a, items["Apple"].ID, false); err != nil {
		t.Fatal(err)
	}
	if _, n, err := svc.ClearChecked(ctx, a); err != nil || n != 0 {
		t.Errorf("clear with nothing ticked: %d %v", n, err)
	}
}
