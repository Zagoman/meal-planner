package database_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Zagoman/meal-planner/internal/testutil"
	"github.com/Zagoman/meal-planner/migrations"
)

// Migration 00002 keeps existing data: dinners_per_week becomes the dinner
// target, the avoid array becomes avoid_items rows, and list items in the
// old "pantry_check" section become Check at home items in their real
// section.
func TestMigration00002KeepsData(t *testing.T) {
	pool := testutil.NewDB(t)
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var hh, plan, meal string
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Old') RETURNING id`).Scan(&hh); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO profiles (household_id, dinners_per_week, servings, avoid)
	      VALUES ($1, 5, 3, ARRAY['Mushrooms', 'mushrooms', 'Fresh coriander (leaves)', ''])`, hh)
	if err := pool.QueryRow(ctx, `INSERT INTO plans (household_id, week_start, status) VALUES ($1, '2026-11-23', 'active') RETURNING id`, hh).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO meals (household_id, plan_id, day, title, protein, minutes, one_pot, servings)
	      VALUES ($1, $2, 'monday', 'Stew', 'other', 30, true, 3) RETURNING id`, hh, plan).Scan(&meal); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ingredients (household_id, meal_id, position, name, normalized_name, qty, unit, section)
	      VALUES ($1, $2, 0, 'salt', 'salt', 0, '', 'spices')`, hh, meal)
	exec(`INSERT INTO list_items (household_id, plan_id, name, normalized_name, section)
	      VALUES ($1, $2, 'Salt', 'salt', 'pantry_check'), ($1, $2, 'Pepper', 'pepper', 'pantry_check')`, hh, plan)

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	var targets string
	var people int
	if err := pool.QueryRow(ctx, `SELECT meal_targets::text, people FROM profiles WHERE household_id = $1`, hh).Scan(&targets, &people); err != nil {
		t.Fatal(err)
	}
	if targets != `{"lunch": 0, "baking": 0, "dinner": 5, "breakfast": 0}` || people != 3 {
		t.Errorf("profile: %s people %d", targets, people)
	}
	rows, err := pool.Query(ctx, `SELECT name, normalized_name FROM avoid_items WHERE household_id = $1 ORDER BY normalized_name`, hh)
	if err != nil {
		t.Fatal(err)
	}
	var avoid []string
	for rows.Next() {
		var name, norm string
		if err := rows.Scan(&name, &norm); err != nil {
			t.Fatal(err)
		}
		avoid = append(avoid, name+"|"+norm)
	}
	rows.Close()
	if len(avoid) != 2 || avoid[0] != "Fresh coriander (leaves)|fresh coriander" || avoid[1] != "Mushrooms|mushrooms" {
		t.Errorf("avoid items: %v", avoid)
	}
	var mealType string
	if err := pool.QueryRow(ctx, `SELECT meal_type FROM meals WHERE id = $1`, meal).Scan(&mealType); err != nil || mealType != "dinner" {
		t.Errorf("meal type %q %v", mealType, err)
	}
	for name, want := range map[string]string{"Salt": "spices", "Pepper": "other"} {
		var section string
		var checkHome bool
		if err := pool.QueryRow(ctx, `SELECT section, check_home FROM list_items WHERE name = $1`, name).Scan(&section, &checkHome); err != nil {
			t.Fatal(err)
		}
		if section != want || !checkHome {
			t.Errorf("%s: section %q check_home %v", name, section, checkHome)
		}
	}
}
