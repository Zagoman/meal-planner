package database_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Zagoman/meal-planner/internal/testutil"
	"github.com/Zagoman/meal-planner/migrations"
)

// Migration 00010 puts in the library only recipes that were cooked, that
// the household added (photo, text, link, by hand) or tweaked. The rest
// stay, linked from their plans, but out of the library.
func TestMigration00010BackfillsLibrary(t *testing.T) {
	pool := testutil.NewDB(t)
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatalf("down to 9: %v", err)
	}
	var hh, plan string
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Old') RETURNING id`).Scan(&hh); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO plans (household_id, week_start, status) VALUES ($1, '2026-11-23', 'active') RETURNING id`, hh).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	recipe := func(title, source string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO recipes (household_id, title, protein, minutes, one_pot, servings, data, source, meal_type, title_key)
		      VALUES ($1, $2, 'other', 30, true, 3, '{}', $3, 'dinner', lower($2)) RETURNING id`, hh, title, source).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := 0
	days := []string{"monday", "tuesday", "wednesday", "thursday"}
	meal := func(recipeID, outcome string) {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO meals (household_id, plan_id, day, title, protein, minutes, one_pot, servings, recipe_id)
		      VALUES ($1, $2, $3, 'x', 'other', 30, true, 3, $4) RETURNING id`, hh, plan, days[day], recipeID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		day++
		if outcome == "" {
			return
		}
		rating := any(nil)
		if outcome == "cooked" {
			rating = 4
		}
		if _, err := pool.Exec(ctx, `INSERT INTO meal_outcomes (household_id, meal_id, status, rating) VALUES ($1, $2, $3, $4)`,
			hh, id, outcome, rating); err != nil {
			t.Fatal(err)
		}
	}
	cooked := recipe("Cooked stew", "claude")
	meal(cooked, "cooked")
	notCooked := recipe("Skipped curry", "claude")
	meal(notCooked, "not_cooked")
	planned := recipe("Planned rice", "claude")
	meal(planned, "")
	unused := recipe("Swapped out soup", "claude")
	photo := recipe("Gran's cake", "photo")
	manual := recipe("Our pancakes", "manual")
	var tweak string
	if err := pool.QueryRow(ctx, `INSERT INTO recipes (household_id, title, protein, minutes, one_pot, servings, data, source, meal_type, title_key, parent_recipe_id)
	      VALUES ($1, 'Planned rice with peas', 'other', 30, true, 3, '{}', 'claude', 'dinner', 'planned rice with peas', $2) RETURNING id`, hh, planned).Scan(&tweak); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	want := map[string]bool{cooked: true, notCooked: false, planned: false, unused: false, photo: true, manual: true, tweak: true}
	rows, err := pool.Query(ctx, `SELECT id::text, title, in_library FROM recipes WHERE household_id = $1`, hh)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, title string
		var in bool
		if err := rows.Scan(&id, &title, &in); err != nil {
			t.Fatal(err)
		}
		n++
		if w, ok := want[id]; !ok || in != w {
			t.Errorf("%s: in_library %v, want %v", title, in, w)
		}
	}
	if n != len(want) {
		t.Errorf("%d recipes after the migration, want %d (none deleted)", n, len(want))
	}
}
