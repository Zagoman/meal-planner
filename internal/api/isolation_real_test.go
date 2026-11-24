package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Zagoman/meal-planner/internal/api"
	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/planner"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

// Claude replies for the real-services run. Household A's seed plan is the
// only one that contains the marker; every later call gets a plain meal, so
// B's own new plan can never look like a leak.
const (
	markerReply = `{"meals":[{"meal_type":"dinner","day":"monday","title":"Marker stew","protein":"fish","minutes":30,"one_pot":true,"servings":2,
		"ingredients":[{"name":"Marker onions","normalized_name":"marker onion","qty":2,"unit":"pcs","section":"a_marker"},
		               {"name":"cream","normalized_name":"cream","qty":200,"unit":"ml","section":"other"}],
		"steps":[{"text":"Cook the marker stew.","timer_min":20,"baby_portion":false}],"after_baby_portion":[]}]}`
	plainReply = `{"meals":[{"meal_type":"dinner","day":"monday","title":"Plain stew","protein":"vegetarian","minutes":25,"one_pot":true,"servings":2,
		"ingredients":[{"name":"rice","normalized_name":"rice","qty":200,"unit":"g","section":"other"},
		               {"name":"cream","normalized_name":"cream","qty":100,"unit":"ml","section":"other"}],
		"steps":[{"text":"Cook it.","timer_min":null,"baby_portion":false}],"after_baby_portion":[]}]}`
)

type realEnv struct {
	*env
	plans *planner.Service
	lists *shopping.Service
	items int
}

// newRealEnv is newEnv with package B's real Plans and Lists (and a scripted
// Claude) instead of the in-memory fakes.
func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	pool := testutil.NewDB(t)
	hub := events.NewHub()
	fc := claude.NewFake(claude.FakeReply{JSON: markerReply})
	for i := 0; i < 6; i++ {
		fc.Push(claude.FakeReply{JSON: plainReply})
	}
	e := &env{t: t, pool: pool, q: db.New(pool), hub: hub}
	e.auth = auth.New(pool, adminEmail)
	e.hh = household.New(pool, baseURL, adminEmail)
	re := &realEnv{
		env:   e,
		plans: planner.New(pool, fc, hub, planner.Options{Timeout: 10 * time.Second}),
		lists: shopping.New(pool, hub),
	}
	deps := app.Deps{
		Config: config.Config{BaseURL: baseURL, AdminEmail: adminEmail},
		Pool:   pool, Events: hub,
		Services: service.Services{
			Auth: e.auth, Households: e.hh, Profiles: e.hh, Feedback: e.hh,
			Plans: re.plans, Lists: re.lists,
		},
	}
	r := chi.NewRouter()
	r.Mount("/api", api.RoutesWithKeepAlive(deps, 50*time.Millisecond))
	e.handler = r
	e.a = e.newParty(testutil.NewHousehold(t, pool, "Amarker"))
	e.b = e.newParty(testutil.NewHousehold(t, pool, "Bee"))
	// The scripted replies are one dinner each.
	pb, err := e.hh.Get(context.Background(), e.b.scope)
	if err != nil {
		t.Fatal(err)
	}
	pb.MealTargets = map[domain.MealType]int{domain.MealDinner: 1}
	if _, err := e.hh.Put(context.Background(), e.b.scope, pb); err != nil {
		t.Fatal(err)
	}
	re.seedA()
	return re
}

func (re *realEnv) seedA() {
	t, ctx, sc := re.t, context.Background(), re.a.scope
	t.Helper()
	p, err := re.hh.Get(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	p.Shop = "Amarker shop"
	p.Avoid = []string{"marker nuts"}
	p.MealTargets = map[domain.MealType]int{domain.MealDinner: 1}
	if _, err := re.hh.Put(ctx, sc, p); err != nil {
		t.Fatal(err)
	}
	if _, err := re.hh.PutSections(ctx, sc, []domain.Section{{Key: "a_marker", Name: "Marker aisle"}, {Key: "other", Name: "Other marker"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := re.hh.PutPantry(ctx, sc, []string{"Marker beans"}); err != nil {
		t.Fatal(err)
	}
	plan, err := re.plans.Generate(ctx, sc, "marker prompt")
	if err != nil {
		t.Fatal(err)
	}
	re.aPlanID, re.aMealID = plan.ID, plan.Meals[0].ID
	list, err := re.lists.Active(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list.Sections {
		for _, it := range s.Items {
			if re.aItem == [16]byte{} {
				re.aItem = it.ID
			}
			re.items++
		}
	}
	if re.aItem == [16]byte{} {
		t.Fatal("seed plan produced an empty list")
	}
}

func (re *realEnv) assertAUnchanged() {
	t, ctx, sc := re.t, context.Background(), re.a.scope
	t.Helper()
	p, err := re.hh.Get(ctx, sc)
	if err != nil || p.Shop != "Amarker shop" || len(p.Avoid) != 1 {
		t.Errorf("A profile changed: %+v %v", p, err)
	}
	cur, err := re.plans.Current(ctx, sc)
	if err != nil || cur.ID != re.aPlanID || len(cur.Meals) != 1 || cur.Meals[0].Title != "Marker stew" {
		t.Errorf("A plan changed: %+v %v", cur, err)
	}
	if m, err := re.plans.Meal(ctx, sc, re.aMealID); err != nil || len(m.Ingredients) != 2 || m.Ingredients[1].NormalizedName != "cream" {
		t.Errorf("A meal changed: %+v %v", m, err)
	}
	list, err := re.lists.Active(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	n, found := 0, false
	for _, s := range list.Sections {
		for _, it := range s.Items {
			n++
			if it.ID == re.aItem {
				found = true
				if it.Checked {
					t.Errorf("A's item was ticked")
				}
			}
		}
	}
	if !found || n != re.items {
		t.Errorf("A list changed: found=%v items=%d want %d", found, n, re.items)
	}
	// recipes: A's seeded meal is in its library; B's calls add none to it.
	for table, want := range map[string]int{"meal_outcomes": 0, "invites": 0, "household_members": 1, "recipes": 1} {
		var got int
		if err := re.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE household_id = $1", sc.HouseholdID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("A %s: %d rows, want %d", table, got, want)
		}
	}
}

// TestHouseholdIsolationRealServices repeats TestHouseholdIsolation with the
// real planner and shopping services wired in, as in cmd/server.
func TestHouseholdIsolationRealServices(t *testing.T) {
	t.Parallel()
	for _, c := range isoCases {
		t.Run(c.method+" "+c.route, func(t *testing.T) {
			t.Parallel()
			t.Run("B cannot touch A", func(t *testing.T) {
				t.Parallel()
				re := newRealEnv(t)
				code, body := re.run(c, re.b)
				if code != c.bWant {
					t.Errorf("B got %d, want %d: %s", code, c.bWant, body)
				}
				if strings.Contains(strings.ToLower(body), marker) || strings.Contains(body, re.aItem.String()) ||
					strings.Contains(body, re.aMealID.String()) || strings.Contains(body, re.aPlanID.String()) {
					t.Errorf("B's response leaks A's data: %s", body)
				}
				if c.route == "/list/stream" && strings.Contains(body, "event:") {
					t.Errorf("B's stream received events: %s", body)
				}
				re.assertAUnchanged()
			})
			t.Run("A on its own data", func(t *testing.T) {
				t.Parallel()
				re := newRealEnv(t)
				code, body := re.run(c, re.a)
				if code != c.aWant {
					t.Errorf("A got %d, want %d: %s", code, c.aWant, body)
				}
			})
		})
	}
}
