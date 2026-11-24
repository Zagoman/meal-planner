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
		{"/list", anna, []string{`sse-connect="/list/stream"`, `sse-swap="list"`, f.seed.FirstHomeItem.Name,
			`id="progress"`, `id="check-home"`, "Check at home", ">Have it<", ">Buy<", `name="section"`,
			`aria-current="page"`, `href="/recipes"`}},
		{"/", anna, []string{`sse-connect="/list/stream"`, `id="progress"`}},
		{"/week", anna, []string{meal.Title, `id="week-meals"`, `id="meal-` + meal.ID.String() + `"`,
			"Dinner · ", "cook them in any order", `id="draft-card"`}},
		{"/recipes", anna, []string{"Recipes", `aria-current="page"`}},
		{"/meals/" + meal.ID.String(), anna, []string{meal.Title, "Baby portion", "after baby portion",
			`data-timer-min="8"`, "data-cook-open", `id="cook"`}},
		{"/profile", anna, []string{"Shop sections", "Move Bakery up", `name="meals_dinner"`, `name="people"`,
			`name="auto_confirm_time"`, `id="avoid-list"`, `href="/household"`, "rice"}},
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
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/list" {
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

func TestDraftReviewFlow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna, ben, carl := f.session(f.seed.Anna), f.session(f.seed.Ben), f.session(f.seed.Carl)
	before := f.seed.HomePlan

	// Week page: card with "make it now", listening on /events.
	rec := f.do(t, req{path: "/week", session: anna})
	mustContain(t, "week", rec.Body.String(), `sse-connect="/events"`, `id="draft-card"`, `data-state="none"`,
		`hx-trigger="sse:plan.updated, sse:job.failed"`, "Make next week&#39;s plan now")
	// No draft yet: the review page goes back to the week.
	if rec = f.do(t, req{path: "/plans/next", session: anna}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/week" {
		t.Errorf("review without draft: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Make a draft (the fake runs the job at once): the card is ready.
	rec = f.do(t, req{method: "POST", path: "/plans/next/generate", session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "card", rec.Body.String(), `data-state="ready"`, "Next week is ready", `href="/plans/next"`)
	if p, _ := f.store.Current(context.Background(), f.seed.Anna); p.ID != before.ID {
		t.Error("a draft replaced this week's plan")
	}

	// Review page: full screen (no tab bar), Ask Claude, meals, preview, confirm.
	rec = f.do(t, req{path: "/plans/next", session: ben})
	body := rec.Body.String()
	mustContain(t, "review", body, "Next week", "‹ Week", `id="ask"`, `id="draft-meals"`, `id="list-preview"`,
		"Shopping list preview", `action="/plans/next/confirm"`, `sse-connect="/events"`)
	if strings.Contains(body, `class="tabs"`) {
		t.Error("review page shows the tab bar")
	}
	r, _ := f.store.Review(context.Background(), f.seed.Anna)
	meal := r.Meals[0]
	mealPath := "/meals/" + meal.ID.String()
	mustContain(t, "review", body, `id="meal-`+meal.ID.String()+`"`, `hx-trigger="sse:meal-`+meal.ID.String()+`"`)

	// Votes from two members; the card names them.
	if rec = f.do(t, req{method: "POST", path: mealPath + "/vote", form: url.Values{"vote": {"+1"}}, session: anna, hx: true}); rec.Code != http.StatusOK {
		t.Fatalf("vote: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, req{method: "POST", path: mealPath + "/vote", form: url.Values{"vote": {"-1"}}, session: ben, hx: true})
	mustContain(t, "vote", rec.Body.String(), "liked by Anna", "disliked by you", "Swapped automatically if still disliked at")
	if rec = f.do(t, req{method: "POST", path: mealPath + "/vote", form: url.Values{"vote": {"9"}}, session: ben, hx: true}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("vote 9: %d", rec.Code)
	}
	// The card partial for SSE refreshes.
	rec = f.do(t, req{path: mealPath + "/card", session: anna, hx: true})
	mustContain(t, "card refetch", rec.Body.String(), "liked by you", "disliked by Ben")

	// Swap keeps the card id, with new content.
	rec = f.do(t, req{method: "POST", path: mealPath + "/swap", session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("swap: %d %s", rec.Code, rec.Body)
	}
	if m, _ := f.store.DraftMeal(context.Background(), f.seed.Anna, meal.ID); m.Title == meal.Title || len(m.Votes) != 0 {
		t.Errorf("swapped meal: %+v", m)
	}

	// Ask Claude: the meals partial; a failing ask shows its error.
	rec = f.do(t, req{method: "POST", path: "/plans/next/ask", form: url.Values{"prompt": {"please fail"}}, session: anna, hx: true})
	mustContain(t, "ask", rec.Body.String(), `id="draft-meals"`)
	rec = f.do(t, req{path: "/plans/next/meals", session: anna, hx: true})
	mustContain(t, "ask failed", rec.Body.String(), "Couldn't change the plan")
	if rec = f.do(t, req{method: "POST", path: "/plans/next/ask", form: url.Values{"prompt": {" "}}, session: anna, hx: true}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty ask: %d", rec.Code)
	}

	// Remove: empty answer, the card disappears; a refetch is empty too.
	last := r.Meals[len(r.Meals)-1]
	rec = f.do(t, req{method: "DELETE", path: "/meals/" + last.ID.String(), session: anna, hx: true})
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("remove: %d %q", rec.Code, rec.Body)
	}
	if rec = f.do(t, req{path: "/meals/" + last.ID.String() + "/card", session: anna, hx: true}); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("removed card refetch: %d %q", rec.Code, rec.Body)
	}

	// List preview: line for htmx, a page otherwise.
	rec = f.do(t, req{path: "/plans/next/list-preview", session: anna, hx: true})
	mustContain(t, "preview line", rec.Body.String(), `id="list-preview"`, " items")
	rec = f.do(t, req{path: "/plans/next/list-preview", session: anna})
	mustContain(t, "preview page", rec.Body.String(), "List preview", "‹ Next week")

	// Another household: 404 for every id of this draft, and its own card.
	for _, c := range []req{
		{method: "POST", path: mealPath + "/vote", form: url.Values{"vote": {"-1"}}},
		{method: "POST", path: mealPath + "/swap"},
		{method: "DELETE", path: mealPath},
		{method: "POST", path: mealPath + "/delete"},
		{path: mealPath + "/card"},
	} {
		c.session, c.hx = carl, true
		if rec = f.do(t, c); c.method != "" && rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as other household: %d", c.method, c.path, rec.Code)
		} else if c.method == "" && (rec.Code != http.StatusOK || rec.Body.Len() != 0) {
			t.Errorf("GET %s as other household: %d %q", c.path, rec.Code, rec.Body)
		}
	}
	rec = f.do(t, req{path: "/plans/next/card", session: carl, hx: true})
	mustContain(t, "other card", rec.Body.String(), `data-state="none"`)
	if rec = f.do(t, req{path: "/plans/next", session: carl}); rec.Code != http.StatusSeeOther {
		t.Errorf("other household review: %d", rec.Code)
	}
	if m, _ := f.store.DraftMeal(context.Background(), f.seed.Anna, meal.ID); m.ID != meal.ID {
		t.Error("other household removed the meal")
	}

	// Confirm from two phones: both land on the week; the draft is the plan.
	for _, sess := range []string{anna, ben} {
		rec = f.do(t, req{method: "POST", path: "/plans/next/confirm", session: sess})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/week" {
			t.Errorf("confirm: %d %q", rec.Code, rec.Header().Get("Location"))
		}
	}
	if p, _ := f.store.Current(context.Background(), f.seed.Anna); p.ID != r.Plan.ID || p.Status != domain.PlanActive {
		t.Errorf("confirmed plan: %+v", p)
	}
	rec = f.do(t, req{path: "/week", session: anna})
	mustContain(t, "week after confirm", rec.Body.String(), `data-state="none"`, `id="week-meals"`)
	// The review's meal list partial now sends the page to the week.
	if rec = f.do(t, req{path: "/plans/next/meals", session: anna, hx: true}); rec.Header().Get("HX-Redirect") != "/week" {
		t.Errorf("meals after confirm: %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
}

func TestRateMeal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna := f.session(f.seed.Anna)
	p := f.seed.HomePlan
	rec := f.do(t, req{method: "POST", path: "/meals/" + p.Meals[1].ID.String() + "/rate",
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
		"meals_breakfast": {"7"}, "meals_lunch": {"0"}, "meals_dinner": {"4"}, "meals_baking": {"1"},
		"people": {"3"}, "spice_level": {"medium"}, "one_pot": {"on"},
		"shop": {"Netto"}, "shopping_day": {"friday"}, "auto_confirm_time": {"07:30"}, "language": {"da"},
		"protein_fish": {"2"}, "protein_vegetarian": {"2"},
	}
	rec := f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "save", rec.Body.String(), "Saved ✓", `name="meals_breakfast"`, `value="07:30"`)
	p, _ := f.store.Get(context.Background(), f.seed.Anna)
	if p.MealTargets[domain.MealBreakfast] != 7 || p.MealTargets[domain.MealDinner] != 4 || p.People != 3 || !p.OnePot ||
		p.ShoppingDay != domain.Friday || p.AutoConfirmTime != "07:30" || p.ProteinMix[domain.ProteinFish] != 2 || p.BabyPortions {
		t.Errorf("profile: %+v", p)
	}
	form.Set("people", "0")
	rec = f.do(t, req{method: "POST", path: "/profile", form: form, session: anna, hx: true})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid: %d", rec.Code)
	}
	mustContain(t, "invalid", rec.Body.String(), "people: must be 1 to 12")

	// Avoid list: add, add again (no duplicate), remove; B cannot remove A's.
	for _, name := range []string{"Mushrooms", "mushroom"} {
		rec = f.do(t, req{method: "POST", path: "/profile/avoid", form: url.Values{"name": {name}}, session: anna, hx: true})
		if rec.Code != http.StatusOK {
			t.Fatalf("add avoid: %d %s", rec.Code, rec.Body)
		}
	}
	mustContain(t, "avoid", rec.Body.String(), `id="avoid-list"`, "Mushrooms")
	items, _ := f.store.AvoidItems(context.Background(), f.seed.Anna)
	if len(items) != 1 {
		t.Fatalf("avoid items: %+v", items)
	}
	carl := f.session(f.seed.Carl)
	if rec = f.do(t, req{method: "DELETE", path: "/profile/avoid/" + items[0].ID.String(), session: carl, hx: true}); rec.Code != http.StatusNotFound {
		t.Errorf("other household deletes avoid item: %d", rec.Code)
	}
	if rec = f.do(t, req{method: "DELETE", path: "/profile/avoid/" + items[0].ID.String(), session: anna, hx: true}); rec.Code != http.StatusOK {
		t.Errorf("delete avoid: %d %s", rec.Code, rec.Body)
	}
	if items, _ = f.store.AvoidItems(context.Background(), f.seed.Anna); len(items) != 0 {
		t.Errorf("avoid not removed: %+v", items)
	}

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
	return readSSEPath(t, srvURL+"/list/stream", session)
}

func readSSEPath(t *testing.T, url, session string) (<-chan sseEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
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

// GET /events names the partial to refresh, and never carries another
// household's events. A vote does not re-render the shopping list stream.
func TestEventsStream(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	srv := httptest.NewServer(f.h)
	defer srv.Close()
	ctx := context.Background()
	if _, err := f.store.RequestDraft(ctx, f.seed.Anna); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestDraft(ctx, f.seed.Carl); err != nil {
		t.Fatal(err)
	}
	r, _ := f.store.Review(ctx, f.seed.Anna)
	theirs, _ := f.store.Review(ctx, f.seed.Carl)

	stream, stop := readSSEPath(t, srv.URL+"/events", f.session(f.seed.Anna))
	defer stop()
	list, stopList := readSSE(t, srv.URL, f.session(f.seed.Anna))
	defer stopList()
	if _, ok := next(t, list, 2*time.Second); !ok {
		t.Fatal("no initial list event")
	}
	time.Sleep(100 * time.Millisecond) // both streams subscribed

	// Another household's vote and plan change: nothing arrives.
	if _, err := f.store.Vote(ctx, f.seed.Carl, theirs.Meals[0].ID, -1); err != nil {
		t.Fatal(err)
	}
	f.hub.Publish(f.seed.Neighbours, events.Event{Kind: events.PlanUpdated})
	if ev, ok := next(t, stream, 300*time.Millisecond); ok {
		t.Fatalf("received another household's event: %+v", ev)
	}

	// Ben votes: meal.updated with the id, then the meal's own event.
	mealID := r.Meals[0].ID.String()
	if _, err := f.store.Vote(ctx, f.seed.Ben, r.Meals[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"meal.updated", "meal-" + mealID} {
		ev, ok := next(t, stream, 2*time.Second)
		if !ok || ev.name != want || ev.data != mealID {
			t.Fatalf("want %s, got %+v", want, ev)
		}
	}
	if ev, ok := next(t, list, 300*time.Millisecond); ok {
		t.Errorf("a vote re-rendered the shopping list: %+v", ev.name)
	}
	// A tick is list.item on /events.
	if _, err := f.store.SetChecked(ctx, f.seed.Ben, f.seed.FirstHomeItem.ID, true); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(t, stream, 2*time.Second); !ok || ev.name != "list.item" || ev.data != f.seed.FirstHomeItem.ID.String() {
		t.Errorf("tick event: %+v", ev)
	}
	// Confirm: list.item (rebuilt) and plan.updated.
	if _, err := f.store.Confirm(ctx, f.seed.Anna); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for range 2 {
		if ev, ok := next(t, stream, 2*time.Second); ok {
			got[ev.name] = true
		}
	}
	if !got["plan.updated"] || !got["list.item"] {
		t.Errorf("confirm events: %v", got)
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

func TestChangePassword(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	phone := f.session(f.seed.Anna)
	laptop := f.session(f.seed.Anna)

	rec := f.do(t, req{path: "/household", session: phone})
	mustContain(t, "household page", rec.Body.String(), "Change password", `name="current_password"`)

	change := func(current, next, confirm string) *httptest.ResponseRecorder {
		return f.do(t, req{method: "POST", path: "/household/password", session: phone, hx: true,
			form: url.Values{"current_password": {current}, "new_password": {next}, "confirm_password": {confirm}}})
	}
	if rec := change(webfake.SeedPassword, "a new password", "another one"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("mismatched confirm: %d", rec.Code)
	} else {
		mustContain(t, "mismatch", rec.Body.String(), "do not match")
	}
	if rec := change("wrong", "a new password", "a new password"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("wrong current: %d", rec.Code)
	} else {
		mustContain(t, "wrong current", rec.Body.String(), "not correct")
	}
	if rec := change(webfake.SeedPassword, "a new password", "a new password"); rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	} else {
		mustContain(t, "changed", rec.Body.String(), "Password changed")
	}
	ctx := context.Background()
	if _, err := f.store.SessionScope(ctx, phone); err != nil {
		t.Errorf("this device was signed out: %v", err)
	}
	if _, err := f.store.SessionScope(ctx, laptop); err == nil {
		t.Error("the other device is still signed in")
	}
	email := ""
	if ms, err := f.store.Members(ctx, f.seed.Anna); err == nil {
		for _, m := range ms {
			if m.ID == f.seed.Anna.MemberID {
				email = m.Email
			}
		}
	}
	if _, _, err := f.store.Login(ctx, email, "a new password"); err != nil {
		t.Errorf("login with new password: %v", err)
	}
	// Cross-site posts are refused like every other form.
	if rec := f.do(t, req{method: "POST", path: "/household/password", session: phone, origin: "https://evil.example",
		form: url.Values{"current_password": {"a new password"}, "new_password": {"pwned pwned"}, "confirm_password": {"pwned pwned"}}}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin change: %d", rec.Code)
	}
}

func TestCheckAtHome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	anna, carl := f.session(f.seed.Anna), f.session(f.seed.Carl)
	l, _ := f.store.Active(context.Background(), f.seed.Anna)
	if len(l.CheckHome) == 0 {
		t.Fatal("seed has no Check at home items")
	}
	home := l.CheckHome[0]
	path := "/list/items/" + home.ID.String() + "/home"
	inSection := func() bool {
		l, _ := f.store.Active(context.Background(), f.seed.Anna)
		for _, s := range l.Sections {
			for _, it := range s.Items {
				if it.ID == home.ID {
					return true
				}
			}
		}
		return false
	}
	if inSection() {
		t.Fatal("unanswered pantry item is on the shopping part of the list")
	}

	// Buy: the item joins its section, and the answer shows as selected.
	rec := f.do(t, req{method: "POST", path: path, form: url.Values{"have_it": {"false"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("buy: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "buy", rec.Body.String(), `id="check-home"`, "1 of ", `class="sb sb-on" aria-pressed="true">Buy`)
	if !inSection() {
		t.Error("Buy did not move the item into its section")
	}
	// Have it hides it again; an empty answer clears it.
	for _, v := range []string{"true", ""} {
		if rec = f.do(t, req{method: "POST", path: path, form: url.Values{"have_it": {v}}, session: anna, hx: true}); rec.Code != http.StatusOK {
			t.Fatalf("have_it=%q: %d", v, rec.Code)
		}
		if inSection() {
			t.Errorf("have_it=%q: item still in a section", v)
		}
	}
	// Without JavaScript: redirect to the list.
	if rec = f.do(t, req{method: "POST", path: path, form: url.Values{"have_it": {"true"}}, session: anna}); rec.Code != http.StatusSeeOther {
		t.Errorf("plain post: %d", rec.Code)
	}
	if rec = f.do(t, req{method: "POST", path: path, form: url.Values{"have_it": {"maybe"}}, session: anna, hx: true}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad value: %d", rec.Code)
	}
	// Another household: 404, and nothing changes.
	if rec = f.do(t, req{method: "POST", path: path, form: url.Values{"have_it": {"false"}}, session: carl, hx: true}); rec.Code != http.StatusNotFound {
		t.Errorf("other household: %d", rec.Code)
	}
	if inSection() {
		t.Error("other household changed the answer")
	}
	// A normal item cannot be answered.
	item := f.seed.FirstHomeItem
	if rec = f.do(t, req{method: "POST", path: "/list/items/" + item.ID.String() + "/home", form: url.Values{"have_it": {"true"}}, session: anna, hx: true}); rec.Code != http.StatusNotFound {
		t.Errorf("normal item: %d", rec.Code)
	}
	// /toggle is the tick route.
	rec = f.do(t, req{method: "POST", path: "/list/items/" + item.ID.String() + "/toggle", form: url.Values{"checked": {"true"}}, session: anna, hx: true})
	if rec.Code != http.StatusOK {
		t.Errorf("toggle: %d", rec.Code)
	}
	mustContain(t, "toggle", rec.Body.String(), `hx-post="/list/items/`+item.ID.String()+`/toggle"`)
}
