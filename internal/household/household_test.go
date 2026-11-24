package household_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"  Tomatoes ":       "tomato",
		"Cherry  Tomatoes":  "cherry tomato",
		"onions":            "onion",
		"Berries":           "berry",
		"pies":              "pie",
		"peaches":           "peach",
		"dishes":            "dish",
		"boxes":             "box",
		"glasses":           "glass",
		"cheeses":           "cheese",
		"Swiss":             "swiss",
		"hummus":            "hummus",
		"couscous":          "couscous",
		"asparagus":         "asparagus",
		"peas":              "pea",
		"Olive Oil":         "olive oil",
		"egg":               "egg",
		"gas":               "gas",
		"   ":               "",
		"red lentils":       "red lentil",
		"BELL PEPPERS":      "bell pepper",
		"potatoes":          "potato",
		"chickpeas":         "chickpea",
		"anchovies":         "anchovy",
		"molasses":          "molasses",
		"sweet\tpotatoes\n": "sweet potato",
	}
	for in, want := range cases {
		got := household.NormalizeName(in)
		if got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
		if again := household.NormalizeName(got); again != got {
			t.Errorf("not idempotent: %q -> %q -> %q", in, got, again)
		}
	}
}

type fixture struct {
	pool *pgxpool.Pool
	q    *db.Queries
	s    *household.Service
	a, b domain.Scope
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testutil.NewDB(t)
	return fixture{
		pool: pool, q: db.New(pool), s: household.New(pool, "https://meals.example.test/", "root@example.test"),
		a: testutil.NewHousehold(t, pool, "Alpha"), b: testutil.NewHousehold(t, pool, "Beta"),
	}
}

func (f fixture) meal(t *testing.T, sc domain.Scope) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	p, err := f.q.CreatePlan(ctx, db.CreatePlanParams{HouseholdID: sc.HouseholdID, WeekStart: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.q.CreateMeal(ctx, db.CreateMealParams{
		HouseholdID: sc.HouseholdID, PlanID: p.ID, Day: pgtype.Text{String: "tuesday", Valid: true}, MealType: "dinner", AddedFrom: "claude",
		Title: "Fish pie", Protein: "fish",
		Minutes: 40, OnePot: false, Servings: 3, AfterBabyPortion: []string{"salt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, ing := range []string{"cod", "potato"} {
		if err := f.q.CreateIngredient(ctx, db.CreateIngredientParams{
			HouseholdID: sc.HouseholdID, MealID: m.ID, Position: int32(i), Name: ing, NormalizedName: ing, Qty: 1.5, Unit: "kg", Section: "produce",
		}); err != nil {
			t.Fatal(err)
		}
	}
	timer := int32(10)
	if err := f.q.CreateStep(ctx, db.CreateStepParams{HouseholdID: sc.HouseholdID, MealID: m.ID, Position: 0, Text: "Boil", TimerMin: &timer, BabyPortion: true}); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func TestCreateHousehold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	if _, err := f.s.Create(ctx, f.a, "New", "x@example.test"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non server admin: %v", err)
	}
	admin := f.a
	admin.ServerAdmin = true
	if _, err := f.s.Create(ctx, admin, " ", "nope"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("invalid: %v", err)
	}
	h, err := f.s.Create(ctx, admin, " Cottage ", "Root@Example.test")
	if err != nil || h.Name != "Cottage" {
		t.Fatalf("create: %+v %v", h, err)
	}
	sc := domain.Scope{HouseholdID: h.ID}
	secs, err := f.s.Sections(ctx, sc)
	if err != nil || len(secs) != len(domain.DefaultSections) || secs[0].Key != domain.DefaultSections[0].Key {
		t.Errorf("sections: %+v %v", secs, err)
	}
	p, err := f.s.Get(ctx, sc)
	if err != nil || p.MealTargets[domain.MealDinner] != 4 || p.People != 2 || p.AutoConfirmTime != "08:00" || p.ShoppingDay != domain.Saturday {
		t.Errorf("profile: %+v %v", p, err)
	}
	m, err := f.q.GetMemberByEmail(ctx, "root@example.test")
	if err != nil || !m.IsServerAdmin {
		t.Errorf("admin email member: %+v %v", m, err)
	}
	// An existing member is reused, not duplicated.
	if _, err := f.s.Create(ctx, admin, "Second", "root@example.test"); err != nil {
		t.Fatal(err)
	}
	hs, _ := f.q.ListMemberHouseholds(ctx, m.ID)
	if len(hs) != 2 {
		t.Errorf("memberships: %+v", hs)
	}
}

func TestBootstrap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	q := db.New(pool)
	if created, err := household.Bootstrap(ctx, pool, ""); err != nil || created {
		t.Fatalf("empty email: %v %v", created, err)
	}
	created, err := household.Bootstrap(ctx, pool, "Admin@Example.test")
	if err != nil || !created {
		t.Fatalf("bootstrap: %v %v", created, err)
	}
	m, err := q.GetMemberByEmail(ctx, "admin@example.test")
	if err != nil || !m.IsServerAdmin || m.PasswordHash != "" {
		t.Fatalf("admin: %+v %v", m, err)
	}
	hs, _ := q.ListMemberHouseholds(ctx, m.ID)
	if len(hs) != 1 || hs[0].Name != "Home" || hs[0].Role != "admin" {
		t.Fatalf("households: %+v", hs)
	}
	if created, err := household.Bootstrap(ctx, pool, "admin@example.test"); err != nil || created {
		t.Fatalf("second run: %v %v", created, err)
	}
	// An existing member who lost the flag gets it back.
	if err := q.SetServerAdmin(ctx, db.SetServerAdminParams{ID: m.ID, IsServerAdmin: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := household.Bootstrap(ctx, pool, "admin@example.test"); err != nil {
		t.Fatal(err)
	}
	if m, _ := q.GetMember(ctx, m.ID); !m.IsServerAdmin {
		t.Error("flag not restored")
	}
}

func TestInvites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	member := f.a
	member.Role = domain.RoleMember
	if _, err := f.s.CreateInvite(ctx, member, domain.RoleMember); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("member creates invite: %v", err)
	}
	inv, err := f.s.CreateInvite(ctx, f.a, "")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Role != domain.RoleMember || inv.URL != "https://meals.example.test/invite/"+inv.Token {
		t.Errorf("invite: %+v", inv)
	}
	var stored int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM invites WHERE token_hash = convert_to($1, 'UTF8')", inv.Token).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("raw token stored: %d %v", stored, err)
	}
	if _, err := f.s.AcceptInvite(ctx, f.b.MemberID, "wrong"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("wrong token: %v", err)
	}
	ms, err := f.s.AcceptInvite(ctx, f.b.MemberID, inv.Token)
	if err != nil || ms.HouseholdID != f.a.HouseholdID || ms.HouseholdName != "Alpha" || ms.Role != domain.RoleMember {
		t.Fatalf("accept: %+v %v", ms, err)
	}
	if _, err := f.s.AcceptInvite(ctx, f.b.MemberID, inv.Token); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("reuse: %v", err)
	}
	members, _ := f.s.Members(ctx, f.a)
	if len(members) != 2 {
		t.Errorf("A members: %+v", members)
	}
	// B's member now sees both households in Memberships; B's household
	// still has only its own member.
	if mss, _ := f.s.Memberships(ctx, f.b); len(mss) != 2 {
		t.Errorf("memberships: %+v", mss)
	}
	if members, _ := f.s.Members(ctx, f.b); len(members) != 1 {
		t.Errorf("B members: %+v", members)
	}

	exp, err := f.s.CreateInvite(ctx, f.a, domain.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE invites SET expires_at = now() - interval '1 minute' WHERE id = $1", exp.ID); err != nil {
		t.Fatal(err)
	}
	c := testutil.NewHousehold(t, f.pool, "Gamma")
	if _, err := f.s.AcceptInvite(ctx, c.MemberID, exp.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expired: %v", err)
	}
	if _, err := f.s.CreateInvite(ctx, f.a, "owner"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("bad role: %v", err)
	}
	// An existing member keeps their role when accepting an admin invite.
	adm, _ := f.s.CreateInvite(ctx, f.a, domain.RoleAdmin)
	if ms, err := f.s.AcceptInvite(ctx, f.b.MemberID, adm.Token); err != nil || ms.Role != domain.RoleMember {
		t.Errorf("re-join: %+v %v", ms, err)
	}
}

func TestUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	if err := f.q.InsertUsage(ctx, db.InsertUsageParams{HouseholdID: f.a.HouseholdID, Kind: "plan", Model: "m", InputTokens: 100, OutputTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Usage(ctx, f.a, time.Time{}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("household admin sees usage: %v", err)
	}
	admin := f.b
	admin.ServerAdmin = true
	rows, err := f.s.Usage(ctx, admin, time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 2 {
		t.Fatalf("usage: %+v %v", rows, err)
	}
	for _, r := range rows {
		if r.HouseholdID == f.a.HouseholdID && (r.Calls != 1 || r.InputTokens != 100 || r.OutputTokens != 50) {
			t.Errorf("A usage: %+v", r)
		}
	}
}

func TestProfileSectionsPantryIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	p, _ := f.s.Get(ctx, f.a)
	p.Shop = "Alpha shop"
	p.ProteinMix = map[domain.Protein]int{domain.ProteinFish: 2}
	if _, err := f.s.Put(ctx, f.a, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutSections(ctx, f.a, []domain.Section{{Key: "alpha", Name: "Alpha"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutPantry(ctx, f.a, []string{"Alpha Salts"}); err != nil {
		t.Fatal(err)
	}
	// B sees and changes only its own.
	pb, _ := f.s.Get(ctx, f.b)
	if pb.Shop != "" || len(pb.ProteinMix) != 0 {
		t.Errorf("B profile: %+v", pb)
	}
	pb.Shop = "Beta shop"
	if _, err := f.s.Put(ctx, f.b, pb); err != nil {
		t.Fatal(err)
	}
	if secs, _ := f.s.Sections(ctx, f.b); len(secs) != len(domain.DefaultSections) {
		t.Errorf("B sections: %+v", secs)
	}
	if _, err := f.s.PutSections(ctx, f.b, []domain.Section{{Key: "beta", Name: "Beta"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutPantry(ctx, f.b, nil); err != nil {
		t.Fatal(err)
	}
	pa, _ := f.s.Get(ctx, f.a)
	secs, _ := f.s.Sections(ctx, f.a)
	pan, _ := f.s.Pantry(ctx, f.a)
	if pa.Shop != "Alpha shop" || pa.ProteinMix[domain.ProteinFish] != 2 || len(secs) != 1 || secs[0].Key != "alpha" ||
		len(pan) != 1 || pan[0].NormalizedName != "alpha salt" {
		t.Errorf("A changed: %+v %+v %+v", pa, secs, pan)
	}

	// Avoid list: Put replaces it, AddAvoid/DeleteAvoid change one entry,
	// and B can neither see nor delete A's entries.
	pa.Avoid = []string{"Mushrooms", "mushroom", " leek "}
	pa.MealTargets = map[domain.MealType]int{domain.MealBreakfast: 7, domain.MealDinner: 4, domain.MealBaking: 1}
	pa.AutoConfirmTime = "7:30"
	got, err := f.s.Put(ctx, f.a, pa)
	if err != nil || len(got.Avoid) != 2 || got.MealTargets[domain.MealBreakfast] != 7 || got.MealTargets[domain.MealLunch] != 0 ||
		got.AutoConfirmTime != "07:30" {
		t.Fatalf("put with avoid: %+v %v", got, err)
	}
	leek, err := f.s.AddAvoid(ctx, f.a, "Leeks", domain.AvoidFromRecipe)
	if err != nil || leek.Name != "leek" {
		t.Errorf("adding a name already on the list returns it: %+v %v", leek, err)
	}
	cor, err := f.s.AddAvoid(ctx, f.a, "Coriander", domain.AvoidFromRecipe)
	if err != nil || cor.Source != domain.AvoidFromRecipe || cor.NormalizedName != "coriander" {
		t.Fatalf("add avoid: %+v %v", cor, err)
	}
	if _, err := f.s.AddAvoid(ctx, f.a, " ", domain.AvoidFromProfile); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("empty avoid: %v", err)
	}
	if items, _ := f.s.AvoidItems(ctx, f.b); len(items) != 0 {
		t.Errorf("B sees A's avoid list: %+v", items)
	}
	if err := f.s.DeleteAvoid(ctx, f.b, cor.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("B deletes A's avoid item: %v", err)
	}
	if err := f.s.DeleteAvoid(ctx, f.a, cor.ID); err != nil {
		t.Fatal(err)
	}
	// nil Avoid leaves the list alone.
	got.Avoid = nil
	if got, _ = f.s.Put(ctx, f.a, got); len(got.Avoid) != 2 {
		t.Errorf("nil avoid changed the list: %+v", got.Avoid)
	}
	bad := []domain.Profile{
		func() domain.Profile { x := pa; x.People = 0; return x }(),
		func() domain.Profile { x := pa; x.MealTargets = map[domain.MealType]int{}; return x }(),
		func() domain.Profile { x := pa; x.MealTargets = map[domain.MealType]int{"dinner": 15}; return x }(),
		func() domain.Profile { x := pa; x.MealTargets = map[domain.MealType]int{"brunch": 2}; return x }(),
		func() domain.Profile { x := pa; x.AutoConfirmTime = "25:00"; return x }(),
		func() domain.Profile { x := pa; x.SpiceLevel = "nuclear"; return x }(),
		func() domain.Profile { x := pa; x.ShoppingDay = "Monday"; return x }(),
		func() domain.Profile { x := pa; x.ProteinMix = map[domain.Protein]int{"tofu": 1}; return x }(),
		func() domain.Profile { x := pa; x.ProteinMix = map[domain.Protein]int{"fish": -1}; return x }(),
		func() domain.Profile { x := pa; x.Shop = strings.Repeat("s", 101); return x }(),
	}
	for i, p := range bad {
		if _, err := f.s.Put(ctx, f.a, p); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("bad profile %d: %v", i, err)
		}
	}
}

func TestFeedbackAndRecipes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	mealA := f.meal(t, f.a)

	fb, err := f.s.Rate(ctx, f.a, mealA, 4, " tasty ")
	if err != nil || fb.Rating != 4 || fb.Note != "tasty" || fb.MealID != mealA {
		t.Fatalf("rate: %+v %v", fb, err)
	}
	if fb2, err := f.s.Rate(ctx, f.a, mealA, 2, ""); err != nil || fb2.ID != fb.ID || fb2.Rating != 2 {
		t.Errorf("re-rate: %+v %v", fb2, err)
	}
	if _, err := f.s.Rate(ctx, f.a, mealA, 0, ""); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("rating 0: %v", err)
	}
	// B cannot rate or save A's meal.
	if _, err := f.s.Rate(ctx, f.b, mealA, 5, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("B rates A's meal: %v", err)
	}
	if _, err := f.s.SaveRecipe(ctx, f.b, mealA); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("B saves A's meal: %v", err)
	}

	r, err := f.s.SaveRecipe(ctx, f.a, mealA)
	if err != nil {
		t.Fatal(err)
	}
	if r.SourceMealID == nil || *r.SourceMealID != mealA || r.Meal.Title != "Fish pie" || len(r.Meal.Ingredients) != 2 ||
		r.Meal.Ingredients[1].Name != "potato" || len(r.Meal.Steps) != 1 || r.Meal.Steps[0].TimerMin == nil ||
		*r.Meal.Steps[0].TimerMin != 10 || !r.Meal.Steps[0].BabyPortion || r.Meal.AfterBabyPortion[0] != "salt" {
		t.Errorf("recipe: %+v", r)
	}
	list, err := f.s.Recipes(ctx, f.a)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Errorf("recipes: %+v %v", list, err)
	}
	if list, _ := f.s.Recipes(ctx, f.b); len(list) != 0 {
		t.Errorf("B sees A's recipes: %+v", list)
	}
	// The recipe outlives its meal.
	if err := f.q.DeleteMeal(ctx, db.DeleteMealParams{ID: mealA, HouseholdID: f.a.HouseholdID}); err != nil {
		t.Fatal(err)
	}
	list, err = f.s.Recipes(ctx, f.a)
	if err != nil || len(list) != 1 || list[0].SourceMealID != nil || list[0].Meal.Title != "Fish pie" {
		t.Errorf("recipe after meal delete: %+v %v", list, err)
	}
}
