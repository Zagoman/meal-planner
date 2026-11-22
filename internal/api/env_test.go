package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/api"
	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

const (
	baseURL    = "https://meals.example.test"
	adminEmail = "root@example.test"
	marker     = "marker" // every piece of household A's data contains it
)

type party struct {
	scope domain.Scope
	token string
}

// env is one fresh database with two households: A (with data everywhere,
// all of it containing "marker") and B (defaults only).
type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	hub     *events.Hub
	fake    *fakePlanner
	auth    *auth.Service
	hh      *household.Service
	handler http.Handler

	a, b                    party
	aPlanID, aMealID, aItem uuid.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewDB(t)
	e := &env{t: t, pool: pool, q: db.New(pool), hub: events.NewHub()}
	e.fake = newFakePlanner(e.hub)
	e.auth = auth.New(pool, adminEmail)
	e.hh = household.New(pool, baseURL, adminEmail)
	deps := app.Deps{
		Config: config.Config{BaseURL: baseURL, AdminEmail: adminEmail},
		Pool:   pool, Events: e.hub,
		Services: service.Services{
			Auth: e.auth, Households: e.hh, Profiles: e.hh, Feedback: e.hh,
			Plans: e.fake, Lists: e.fake,
		},
	}
	r := chi.NewRouter()
	r.Mount("/api", api.RoutesWithKeepAlive(deps, 50*time.Millisecond))
	e.handler = r

	e.a = e.newParty(testutil.NewHousehold(t, pool, "Amarker"))
	e.b = e.newParty(testutil.NewHousehold(t, pool, "Bee"))
	e.seedA()
	return e
}

func (e *env) newParty(sc domain.Scope) party {
	e.t.Helper()
	tok, err := e.auth.CreateAPIToken(context.Background(), sc, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return party{scope: sc, token: tok}
}

func (e *env) seedA() {
	t, ctx, sc := e.t, context.Background(), e.a.scope
	t.Helper()
	p, err := e.hh.Get(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	p.Shop = "Amarker shop"
	p.Avoid = []string{"marker nuts"}
	if _, err := e.hh.Put(ctx, sc, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hh.PutSections(ctx, sc, []domain.Section{{Key: "a_marker", Name: "Marker aisle"}, {Key: "other", Name: "Other marker"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.hh.PutPantry(ctx, sc, []string{"Marker beans"}); err != nil {
		t.Fatal(err)
	}
	plan, err := e.q.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: sc.HouseholdID, WeekStart: time.Now(), Prompt: "marker prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.ActivatePlan(ctx, db.ActivatePlanParams{ID: plan.ID, HouseholdID: sc.HouseholdID}); err != nil {
		t.Fatal(err)
	}
	meal, err := e.q.CreateMeal(ctx, db.CreateMealParams{
		HouseholdID: sc.HouseholdID, PlanID: plan.ID, Day: "monday", Title: "Marker stew",
		Protein: "fish", Minutes: 30, OnePot: true, Servings: 2, AfterBabyPortion: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.aPlanID, e.aMealID = plan.ID, meal.ID
	e.aItem = e.fake.seed(sc.HouseholdID, plan.ID, meal.ID, "marker prompt", "Marker onions")
}

// do sends one request with the given bearer token ("" for none).
func (e *env) do(token, method, path, body string) (int, string) {
	e.t.Helper()
	return e.doCtx(context.Background(), token, method, path, body)
}

func (e *env) doCtx(ctx context.Context, token, method, path, body string) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(ctx, method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// assertAUnchanged checks every piece of household A's data is as seeded.
func (e *env) assertAUnchanged() {
	t, ctx, sc := e.t, context.Background(), e.a.scope
	t.Helper()
	p, err := e.hh.Get(ctx, sc)
	if err != nil || p.Shop != "Amarker shop" || len(p.Avoid) != 1 {
		t.Errorf("A profile changed: %+v %v", p, err)
	}
	secs, err := e.hh.Sections(ctx, sc)
	if err != nil || len(secs) != 2 || secs[0].Key != "a_marker" {
		t.Errorf("A sections changed: %+v %v", secs, err)
	}
	pan, err := e.hh.Pantry(ctx, sc)
	if err != nil || len(pan) != 1 || pan[0].NormalizedName != "marker bean" {
		t.Errorf("A pantry changed: %+v %v", pan, err)
	}
	if it, ok := e.fake.item(e.aItem); !ok || it.Checked {
		t.Errorf("A list item changed or deleted: %+v %v", it, ok)
	}
	if n := e.fake.itemCount(sc.HouseholdID); n != 1 {
		t.Errorf("A list has %d items, want 1", n)
	}
	if cur := e.fake.currentPlan(sc.HouseholdID); cur.ID != e.aPlanID {
		t.Errorf("A current plan replaced: %v", cur.ID)
	}
	if d := e.fake.swappedDay(e.aPlanID); d != "" {
		t.Errorf("A meal swapped on %s", d)
	}
	if title := e.fake.mealTitle(e.aMealID); title != "Seeded marker prompt" {
		t.Errorf("A meal changed: %q", title)
	}
	for table, want := range map[string]int{"feedback": 0, "invites": 0, "household_members": 1, "recipes": 0} {
		var n int
		if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE household_id = $1", sc.HouseholdID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("A %s: %d rows, want %d", table, n, want)
		}
	}
	var households int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM households").Scan(&households); err != nil || households != 2 {
		t.Errorf("households: %d, want 2 (%v)", households, err)
	}
}
