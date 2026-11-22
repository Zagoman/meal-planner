package web_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/web"
	"github.com/Zagoman/meal-planner/internal/web/webfake"
)

const origin = "http://meals.example.test"

type fixture struct {
	h     http.Handler
	store *webfake.Store
	hub   *events.Hub
	seed  webfake.Seeded
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	hub := events.NewHub()
	store := webfake.New(hub)
	seed := store.Seed()
	h := web.Routes(app.Deps{
		Config:   config.Config{BaseURL: origin, GenerateTimeout: 5 * time.Second},
		Services: store.Services(),
		Events:   hub,
	})
	return &fixture{h: h, store: store, hub: hub, seed: seed}
}

type req struct {
	method, path string
	form         url.Values
	session      string
	hx           bool
	origin       string // default: same origin for unsafe methods
}

func (f *fixture) do(t *testing.T, q req) *httptest.ResponseRecorder {
	t.Helper()
	if q.method == "" {
		q.method = http.MethodGet
	}
	var body *strings.Reader
	if q.form != nil {
		body = strings.NewReader(q.form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(q.method, origin+q.path, body)
	if q.form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if q.method != http.MethodGet {
		o := q.origin
		if o == "" {
			o = origin
		}
		r.Header.Set("Origin", o)
	}
	if q.session != "" {
		r.AddCookie(&http.Cookie{Name: web.CookieName, Value: q.session})
	}
	if q.hx {
		r.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func (f *fixture) session(sc domain.Scope) string { return f.store.NewSession(sc) }

func mustContain(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: body does not contain %q\n%.600s", what, w, body)
		}
	}
}

func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, p := range []string{"/", "/week", "/meals/" + uuid.NewString(), "/profile", "/household"} {
		rec := f.do(t, req{path: p})
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
			t.Errorf("GET %s: %d %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := f.do(t, req{path: "/week"})
	if got := rec.Header().Get("Location"); got != "/login?next=%2Fweek" {
		t.Errorf("next not kept: %q", got)
	}
	// htmx requests get HX-Redirect instead of a redirect htmx would swap in.
	rec = f.do(t, req{method: "POST", path: "/list/items/" + f.seed.FirstHomeItem.ID.String() + "/check",
		form: url.Values{"checked": {"true"}}, hx: true})
	if rec.Code != http.StatusNoContent || !strings.HasPrefix(rec.Header().Get("HX-Redirect"), "/login") {
		t.Errorf("htmx tick: %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	if rec := f.do(t, req{path: "/list/stream"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("stream: %d", rec.Code)
	}
	// An unknown session cookie is cleared.
	rec = f.do(t, req{path: "/", session: "bogus"})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("bogus session: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
}

func TestPagesRender(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	meal := f.seed.HomePlan.Meals[0]
	cases := []struct {
		path    string
		session string
		wants   []string
	}{
		{"/", anna, []string{`sse-connect="/list/stream"`, `sse-swap="list"`, f.seed.FirstHomeItem.Name,
			"Check the pantry", `name="section"`, `aria-current="page"`}},
		{"/week", anna, []string{"Make a new plan", meal.Title, `hx-post="/week/swap"`, `name="rating"`, `hx-indicator="#gen-progress"`}},
		{"/meals/" + meal.ID.String(), anna, []string{meal.Title, "Baby portion", "after baby portion",
			`data-timer-min="8"`, "data-cook-open", `id="cook"`}},
		{"/profile", anna, []string{"Shop sections", "Move Bakery up", `name="dinners_per_week"`, "rice"}},
		{"/household", anna, []string{"Home", "Ben", "Invite someone", "Switch", "API tokens", `action="/logout"`}},
		{"/logout", anna, []string{`action="/logout"`}},
		{"/login", "", []string{`action="/login"`, `name="password"`}},
		{"/invite/sometoken", "", []string{"I have an account", "new here", `value="/invite/sometoken"`}},
		{"/invite/sometoken", anna, []string{"Join household", `action="/invite/sometoken/accept"`}},
	}
	for _, c := range cases {
		rec := f.do(t, req{path: c.path, session: c.session})
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", c.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: content type %q", c.path, ct)
		}
		mustContain(t, c.path, rec.Body.String(), c.wants...)
	}
	// The API key or any Config never reaches a page.
	rec := f.do(t, req{path: "/household", session: anna})
	if strings.Contains(rec.Body.String(), "ANTHROPIC") {
		t.Error("config leaked into page")
	}
	// Member (not admin) sees no invite form.
	rec = f.do(t, req{path: "/household", session: f.session(f.seed.Ben)})
	if strings.Contains(rec.Body.String(), "Invite someone") {
		t.Error("member sees invite form")
	}
}

func TestTickReturnsItemPartial(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	it := f.seed.FirstHomeItem
	path := "/list/items/" + it.ID.String() + "/check"

	rec := f.do(t, req{method: "POST", path: path, form: url.Values{"checked": {"true"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("tick: %d %s", rec.Code, rec.Body)
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.HasPrefix(body, `<li id="item-`+it.ID.String()+`" class="item checked">`) || strings.Contains(body, "<html") {
		t.Errorf("not the item partial: %.300s", body)
	}
	// The next tap posts the opposite state.
	mustContain(t, "tick", body, `hx-vals="{&#34;checked&#34;:&#34;false&#34;}"`, `aria-pressed="true"`)
	l, _ := f.store.Active(context.Background(), f.seed.Anna)
	if !l.Sections[0].Items[0].Checked {
		t.Error("not ticked in store")
	}
	// Setting (not toggling) makes a replayed offline tick idempotent.
	f.do(t, req{method: "POST", path: path, form: url.Values{"checked": {"true"}}, session: anna, hx: true})
	l, _ = f.store.Active(context.Background(), f.seed.Anna)
	if !l.Sections[0].Items[0].Checked {
		t.Error("second identical tick un-ticked")
	}

	// Without JavaScript: redirect back to the list.
	rec = f.do(t, req{method: "POST", path: path, form: url.Values{"checked": {"false"}}, session: anna})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("plain post: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = f.do(t, req{method: "POST", path: path, form: url.Values{"checked": {"maybe"}}, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("bad value: %d %q", rec.Code, rec.Header().Get("HX-Retarget"))
	}
}

func TestAddAndDeleteItem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	rec := f.do(t, req{method: "POST", path: "/list/items", form: url.Values{"name": {"Oat milk"}, "section": {"dairy_eggs"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "add", rec.Body.String(), "Oat milk", `class="del"`)
	// Only manual items have a delete button, so this is Oat milk's.
	id := regexp.MustCompile(`/list/items/([0-9a-f-]{36})/delete`).FindStringSubmatch(rec.Body.String())
	if id == nil {
		t.Fatal("no item id in partial")
	}
	rec = f.do(t, req{method: "POST", path: "/list/items/" + id[1] + "/delete", session: anna, hx: true})
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("delete: %d %q", rec.Code, rec.Body)
	}
	rec = f.do(t, req{method: "POST", path: "/list/items", form: url.Values{"name": {"  "}}, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty name: %d", rec.Code)
	}
}

func TestCSRFRejectsCrossOrigin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	path := "/list/items/" + f.seed.FirstHomeItem.ID.String() + "/check"
	rec := f.do(t, req{method: "POST", path: path, form: url.Values{"checked": {"true"}}, session: anna, origin: "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin post: %d", rec.Code)
	}
	r := httptest.NewRequest("POST", origin+path, strings.NewReader("checked=true"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(&http.Cookie{Name: web.CookieName, Value: anna})
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("sec-fetch-site cross-site: %d", rec.Code)
	}
	l, _ := f.store.Active(context.Background(), f.seed.Anna)
	if l.Sections[0].Items[0].Checked {
		t.Error("cross-site request changed data")
	}
	// Login is protected too (login CSRF).
	rec = f.do(t, req{method: "POST", path: "/login", form: url.Values{"email": {"anna@example.test"}, "password": {webfake.SeedPassword}}, origin: "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin login: %d", rec.Code)
	}
}

func TestLoginLogout(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rec := f.do(t, req{method: "POST", path: "/login", form: url.Values{"email": {"anna@example.test"}, "password": {"nope"}}})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}
	mustContain(t, "wrong password", rec.Body.String(), "Wrong email or password", `value="anna@example.test"`)

	rec = f.do(t, req{method: "POST", path: "/login", form: url.Values{
		"email": {"anna@example.test"}, "password": {webfake.SeedPassword}, "next": {"//evil.example/"}}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.CookieName {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.MaxAge != 30*24*3600 || sess.Secure {
		t.Fatalf("cookie: %+v", sess)
	}
	rec = f.do(t, req{method: "POST", path: "/login", form: url.Values{
		"email": {"anna@example.test"}, "password": {webfake.SeedPassword}, "next": {"/week"}}})
	if rec.Header().Get("Location") != "/week" {
		t.Errorf("next: %q", rec.Header().Get("Location"))
	}
	if rec := f.do(t, req{path: "/", session: sess.Value}); rec.Code != http.StatusOK {
		t.Errorf("after login: %d", rec.Code)
	}
	rec = f.do(t, req{method: "POST", path: "/logout", session: sess.Value})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("logout: %d %q %q", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Set-Cookie"))
	}
	if rec := f.do(t, req{path: "/", session: sess.Value}); rec.Code != http.StatusSeeOther {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestSecureCookieWithHTTPSBaseURL(t *testing.T) {
	t.Parallel()
	hub := events.NewHub()
	store := webfake.New(hub)
	store.Seed()
	h := web.Routes(app.Deps{Config: config.Config{BaseURL: "https://meals.example.test"}, Services: store.Services(), Events: hub})
	r := httptest.NewRequest("POST", "https://meals.example.test/login",
		strings.NewReader(url.Values{"email": {"anna@example.test"}, "password": {webfake.SeedPassword}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://meals.example.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "Secure") {
		t.Errorf("cookie not secure: %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestWeekGenerateSwapRate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	before := f.seed.HomePlan

	// Failure: flash error, plan unchanged.
	rec := f.do(t, req{method: "POST", path: "/week/generate", form: url.Values{"prompt": {"please fail"}}, session: anna, hx: true})
	if rec.Code != http.StatusBadGateway || rec.Header().Get("HX-Retarget") != "#flash" {
		t.Errorf("failed generate: %d %q", rec.Code, rec.Header().Get("HX-Retarget"))
	}
	mustContain(t, "failed generate", rec.Body.String(), "current plan is unchanged")
	if p, _ := f.store.Current(context.Background(), f.seed.Anna); p.ID != before.ID {
		t.Error("failed generation replaced the plan")
	}

	// Success: the #week partial with the new plan.
	rec = f.do(t, req{method: "POST", path: "/week/generate", form: url.Values{"prompt": {"more fish"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "generate", rec.Body.String(), `<div id="week">`)
	p, _ := f.store.Current(context.Background(), f.seed.Anna)
	if p.ID == before.ID {
		t.Error("plan not replaced")
	}

	// Swap returns the new meal card for that day.
	rec = f.do(t, req{method: "POST", path: "/week/swap", form: url.Values{"plan_id": {p.ID.String()}, "day": {"monday"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("swap: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "swap", rec.Body.String(), `<article class="meal card" id="meal-monday">`)
	if strings.Contains(rec.Body.String(), p.Meals[0].Title) {
		t.Error("swap returned the same meal")
	}

	// Rate.
	rec = f.do(t, req{method: "POST", path: "/meals/" + p.Meals[1].ID.String() + "/rate",
		form: url.Values{"rating": {"4"}, "note": {"baby loved it"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("rate: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "rate", rec.Body.String(), "Rated 4/5", "baby loved it")
	if fb := f.store.FeedbackFor(f.seed.Home); len(fb) != 1 || fb[0].Rating != 4 {
		t.Errorf("feedback: %+v", fb)
	}
	rec = f.do(t, req{method: "POST", path: "/meals/" + p.Meals[1].ID.String() + "/rate", form: url.Values{"rating": {"9"}}, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("rating 9: %d", rec.Code)
	}
}

func TestProfileForms(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	form := url.Values{
		"dinners_per_week": {"4"}, "servings": {"3"}, "spice_level": {"medium"}, "one_pot": {"on"},
		"avoid": {"mushrooms\ncoriander"}, "shop": {"Netto"}, "shopping_day": {"friday"}, "language": {"da"},
		"protein_fish": {"2"}, "protein_vegetarian": {"2"},
	}
	rec := f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "save", rec.Body.String(), "Saved ✓", "mushrooms\ncoriander")
	p, _ := f.store.Get(context.Background(), f.seed.Anna)
	if p.DinnersPerWeek != 4 || !p.OnePot || p.ShoppingDay != domain.Friday || len(p.Avoid) != 2 || p.ProteinMix[domain.ProteinFish] != 2 || p.BabyPortions {
		t.Errorf("profile: %+v", p)
	}
	form.Set("dinners_per_week", "9")
	rec = f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid: %d", rec.Code)
	}
	mustContain(t, "invalid", rec.Body.String(), "dinners_per_week: must be 1 to 7")

	rec = f.do(t, req{method: "POST", path: "/profile/sections/move", form: url.Values{"key": {"bakery"}, "dir": {"up"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d", rec.Code)
	}
	secs, _ := f.store.Sections(context.Background(), f.seed.Anna)
	if secs[0].Key != "bakery" || secs[1].Key != "produce" || secs[0].SortOrder >= secs[1].SortOrder {
		t.Errorf("order: %+v", secs[:2])
	}

	rec = f.do(t, req{method: "POST", path: "/profile/pantry", form: url.Values{"pantry": {"salt\nolive oil\n\nsalt"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("pantry: %d", rec.Code)
	}
	if pan, _ := f.store.Pantry(context.Background(), f.seed.Anna); len(pan) != 2 {
		t.Errorf("pantry: %+v", pan)
	}
}

var inviteURL = regexp.MustCompile(`value="` + regexp.QuoteMeta(origin) + `/invite/([A-Za-z0-9_-]+)"`)

func TestInviteFlow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	rec := f.do(t, req{method: "POST", path: "/household/invites", form: url.Values{"role": {"member"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	m := inviteURL.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no invite link: %s", rec.Body)
	}
	mustContain(t, "invite", rec.Body.String(), "data-copy", "shown only once")

	// Carl (another household) accepts while logged in.
	carl := f.session(f.seed.Carl)
	rec = f.do(t, req{method: "POST", path: "/invite/" + m[1] + "/accept", session: carl})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	s, _ := f.store.SessionScope(context.Background(), carl)
	if s.HouseholdID != f.seed.Home || s.Role != domain.RoleMember {
		t.Errorf("session not switched to the new household: %+v", s)
	}
	// Single use.
	rec = f.do(t, req{method: "POST", path: "/invite/" + m[1] + "/accept", session: carl})
	if rec.Code != http.StatusNotFound {
		t.Errorf("reuse: %d", rec.Code)
	}
	mustContain(t, "reuse", rec.Body.String(), "expired or was already used")

	// A new person signs up from an invite.
	rec = f.do(t, req{method: "POST", path: "/household/invites", form: url.Values{"role": {"member"}}, session: anna, hx: true})
	tok := inviteURL.FindStringSubmatch(rec.Body.String())[1]
	rec = f.do(t, req{method: "POST", path: "/invite/" + tok + "/signup", form: url.Values{
		"name": {"Dora"}, "email": {"dora@example.test"}, "password": {"long enough pw"}}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Set-Cookie"), web.CookieName+"=") {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}

	// A plain member cannot create invites.
	rec = f.do(t, req{method: "POST", path: "/household/invites", form: url.Values{"role": {"admin"}}, session: f.session(f.seed.Ben), hx: true})
	if rec.Code != http.StatusForbidden {
		t.Errorf("member invite: %d", rec.Code)
	}
}

func TestSwitchHouseholdAndTokens(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	rec := f.do(t, req{method: "POST", path: "/household/switch", form: url.Values{"household_id": {f.seed.Neighbours.String()}}, session: anna})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("switch: %d", rec.Code)
	}
	if s, _ := f.store.SessionScope(context.Background(), anna); s.HouseholdID != f.seed.Neighbours {
		t.Errorf("not switched: %+v", s)
	}
	rec = f.do(t, req{method: "POST", path: "/household/tokens", form: url.Values{"name": {"script"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("token: %d", rec.Code)
	}
	tok := regexp.MustCompile(`value="([A-Za-z0-9_-]{40,})"`).FindStringSubmatch(rec.Body.String())
	if tok == nil {
		t.Fatalf("no token shown: %s", rec.Body)
	}
	if s, err := f.store.BearerScope(context.Background(), tok[1]); err != nil || s.HouseholdID != f.seed.Neighbours {
		t.Errorf("token scope: %+v %v", s, err)
	}
}

// TestHouseholdIsolation: every endpoint that takes an ID answers 404 for
// another household's row and changes nothing.
func TestHouseholdIsolation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ben := f.session(f.seed.Ben) // Home only
	theirItem := f.seed.FirstNeighbourItem.ID.String()
	theirMeal := f.seed.NeighboursPlan.Meals[0].ID.String()
	cases := []req{
		{method: "POST", path: "/list/items/" + theirItem + "/check", form: url.Values{"checked": {"true"}}},
		{method: "POST", path: "/list/items/" + theirItem + "/delete"},
		{method: "GET", path: "/meals/" + theirMeal},
		{method: "POST", path: "/meals/" + theirMeal + "/rate", form: url.Values{"rating": {"1"}}},
		{method: "POST", path: "/week/swap", form: url.Values{"plan_id": {f.seed.NeighboursPlan.ID.String()}, "day": {"monday"}}},
		{method: "POST", path: "/household/switch", form: url.Values{"household_id": {f.seed.Neighbours.String()}}},
	}
	for _, c := range cases {
		for _, hx := range []bool{false, true} {
			c.session, c.hx = ben, hx
			rec := f.do(t, c)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s (hx=%v): %d, want 404", c.method, c.path, hx, rec.Code)
			}
		}
	}
	l, _ := f.store.Active(context.Background(), f.seed.Carl)
	if l.Sections[0].Items[0].Checked {
		t.Error("other household's item was ticked")
	}
	if fb := f.store.FeedbackFor(f.seed.Neighbours); len(fb) != 0 {
		t.Error("other household's meal was rated")
	}
	if p, _ := f.store.Current(context.Background(), f.seed.Carl); p.Meals[0].ID != f.seed.NeighboursPlan.Meals[0].ID {
		t.Error("other household's meal was swapped")
	}
	if s, _ := f.store.SessionScope(context.Background(), ben); s.HouseholdID != f.seed.Home {
		t.Error("switched into a household without membership")
	}
	// Pages show only the caller's household.
	rec := f.do(t, req{path: "/household", session: ben})
	if strings.Contains(rec.Body.String(), "Carl") || strings.Contains(rec.Body.String(), "Neighbours") {
		t.Error("household page shows another household")
	}
	rec = f.do(t, req{path: "/", session: ben})
	if strings.Contains(rec.Body.String(), theirItem) {
		t.Error("list shows another household's item")
	}
}

type sseEvent struct{ name, data string }

func readSSE(t *testing.T, srvURL, session string) (<-chan sseEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", srvURL+"/list/stream", nil)
	r.AddCookie(&http.Cookie{Name: web.CookieName, Value: session})
	res, err := http.DefaultClient.Do(r) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		_ = res.Body.Close()
		cancel()
		t.Fatalf("stream: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 16)
	go func() {
		defer close(out)
		defer func() { _ = res.Body.Close() }()
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev sseEvent
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			case line == "" && ev.name != "":
				ev.data = strings.Join(data, "\n")
				out <- ev
				ev, data = sseEvent{}, nil
			}
		}
	}()
	return out, cancel
}

func next(t *testing.T, ch <-chan sseEvent, wait time.Duration) (sseEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(wait):
		return sseEvent{}, false
	}
}

func TestSSEOnlyOwnHousehold(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()

	stream, stop := readSSE(t, srv.URL, f.session(f.seed.Anna))
	defer stop()

	first, ok := next(t, stream, 2*time.Second)
	if !ok || first.name != "list" || !strings.Contains(first.data, "item-"+f.seed.FirstHomeItem.ID.String()) {
		t.Fatalf("initial event: %+v", first)
	}

	// Changes in the other household reach no one in this one.
	if _, err := f.store.SetChecked(context.Background(), f.seed.Carl, f.seed.FirstNeighbourItem.ID, true); err != nil {
		t.Fatal(err)
	}
	f.hub.Publish(f.seed.Neighbours, events.Event{Kind: events.ListRebuilt})
	if ev, ok := next(t, stream, 300*time.Millisecond); ok {
		t.Fatalf("received another household's event: %+v", ev)
	}

	// A tick by Ben (same household) arrives as a list partial with the item ticked.
	if _, err := f.store.SetChecked(context.Background(), f.seed.Ben, f.seed.FirstHomeItem.ID, true); err != nil {
		t.Fatal(err)
	}
	ev, ok := next(t, stream, 2*time.Second)
	if !ok || ev.name != "list" {
		t.Fatalf("no event for own household: %+v", ev)
	}
	mustContain(t, "sse", ev.data, `<li id="item-`+f.seed.FirstHomeItem.ID.String()+`" class="item checked">`, "1 of")
	if strings.Contains(ev.data, f.seed.FirstNeighbourItem.ID.String()) {
		t.Error("stream contains another household's item")
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rec := f.do(t, req{path: "/nope", session: f.session(f.seed.Anna)})
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
	rec = f.do(t, req{path: "/meals/not-a-uuid", session: f.session(f.seed.Anna)})
	if rec.Code != http.StatusNotFound {
		t.Errorf("bad uuid: %d", rec.Code)
	}
}

func TestStaticAndServiceWorker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for path, want := range map[string]string{
		"/sw.js":                       "mp-pages-",
		"/static/app.js":               "mp:tickQueue",
		"/static/manifest.webmanifest": "Meal Planner",
		"/static/htmx-ext-sse.js":      "sse",
	} {
		rec := f.do(t, req{path: path})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if rec := f.do(t, req{path: "/sw.js"}); rec.Header().Get("Service-Worker-Allowed") != "/" {
		t.Error("sw scope header missing")
	}
}
