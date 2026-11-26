package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/jobs"
	"github.com/Zagoman/meal-planner/internal/mail"
	"github.com/Zagoman/meal-planner/internal/planner"
	"github.com/Zagoman/meal-planner/internal/server"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
	"github.com/Zagoman/meal-planner/internal/testutil"
	"github.com/Zagoman/meal-planner/internal/web"
)

const weekJSON = `{"meals":[
 {"meal_type":"dinner","day":"monday","title":"Chicken traybake","protein":"chicken","minutes":30,"one_pot":true,"servings":3,
  "ingredients":[{"name":"chicken thighs","normalized_name":"chicken thigh","qty":600,"unit":"g","section":"meat_fish"},
                 {"name":"onions","normalized_name":"onion","qty":2,"unit":"pcs","section":"produce"}],
  "steps":[{"text":"Roast everything.","timer_min":35,"baby_portion":false}],"after_baby_portion":[]},
 {"meal_type":"dinner","day":"tuesday","title":"Fish pie","protein":"fish","minutes":40,"one_pot":true,"servings":3,
  "ingredients":[{"name":"cod","normalized_name":"cod","qty":500,"unit":"g","section":"meat_fish"},
                 {"name":"potatoes","normalized_name":"potato","qty":800,"unit":"g","section":"produce"}],
  "steps":[{"text":"Bake.","timer_min":30,"baby_portion":false}],"after_baby_portion":[]},
 {"meal_type":"baking","day":null,"title":"Rye bread","protein":"vegetarian","minutes":90,"one_pot":true,"servings":1,
  "ingredients":[{"name":"rye flour","normalized_name":"rye flour","qty":500,"unit":"g","section":"dry_goods"},
                 {"name":"water","normalized_name":"water","qty":4,"unit":"dl","section":"other"}],
  "steps":[{"text":"Mix and bake.","timer_min":60,"baby_portion":false}],"after_baby_portion":[]}]}`

type browser struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (b *browser) do(method, path string, form url.Values, hx bool) (int, string, http.Header) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, b.base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw), res.Header
}

func (b *browser) expect(method, path string, form url.Values, hx bool, code int, contains ...string) (string, http.Header) {
	b.t.Helper()
	got, body, h := b.do(method, path, form, hx)
	if got != code {
		b.t.Fatalf("%s %s: %d, want %d (Location %q)\n%s", method, path, got, code, h.Get("Location"), body[:min(400, len(body))])
	}
	for _, c := range contains {
		if !strings.Contains(body, c) {
			b.t.Fatalf("%s %s: body lacks %q", method, path, c)
		}
	}
	return body, h
}

var authLink = regexp.MustCompile(`https://meals\.example\.test/auth/([A-Za-z0-9_-]{43})`)

// Window 4 "done when": a second household joins from an invite, answers
// the four questions, gets a draft email, and completes a shop. Another
// household sees none of it.
func TestSecondHouseholdJoinsAndShops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	hub := events.NewHub()
	rec := &mail.Recorder{}
	const base = "https://meals.example.test"
	fc := claude.NewFake(claude.FakeReply{JSON: weekJSON})
	worker := jobs.NewWorker(pool, jobs.Options{})
	plans := planner.New(pool, fc, hub, planner.Options{Timeout: 10 * time.Second, Mailer: rec, BaseURL: base})
	plans.RegisterJobs(worker)
	hh := household.New(pool, base, "")
	services := service.NewUnimplemented()
	services.Auth = auth.New(pool, "").WithMail(rec, base)
	services.Households, services.Profiles, services.Feedback = hh, hh, hh
	services.Plans, services.Drafts, services.Outcomes, services.Library = plans, plans, plans, plans
	services.Lists = shopping.New(pool, hub)
	srv := httptest.NewServer(server.New(app.Deps{Config: config.Config{BaseURL: base}, Pool: pool, Services: services, Events: hub}))
	defer srv.Close()

	// Household A already exists, with a member who must never see B.
	other := testutil.NewHousehold(t, pool, "Alpha")

	// The server admin creates household B: a one-time invite link.
	h, err := hh.Create(ctx, domain.Scope{ServerAdmin: true}, "Bravo", "")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(h.Invite.URL, base+"/join/")

	// H1: join with name and email; a sign-in link arrives by email.
	b := newBrowser(t, srv.URL)
	b.expect("GET", "/join/"+token, nil, false, 200, "You're invited to Bravo", "Email me a sign-in link")
	b.expect("POST", "/join/"+token, url.Values{"name": {"Bea"}, "email": {"bea@example.test"}}, false, 200, "Check your inbox", "bea@example.test")
	msg, err := rec.Last("bea@example.test")
	if err != nil || msg.Subject != "Your sign-in link" {
		t.Fatalf("sign-in email: %+v %v", msg, err)
	}
	link := authLink.FindStringSubmatch(msg.Text)[1]
	// Opening the link does not use it (mail scanners); the button does.
	b.expect("GET", "/auth/"+link, nil, false, 200, `action="/auth/`+link+`"`)
	_, hdr := b.expect("POST", "/auth/"+link, nil, false, 303)
	if hdr.Get("Location") != "/week" {
		t.Fatalf("after sign in: %q", hdr.Get("Location"))
	}
	if code, _, _ := newBrowser(t, srv.URL).do("POST", "/auth/"+link, nil, false); code != 404 {
		t.Errorf("link used twice: %d", code)
	}

	// H2: a new household goes to the four questions.
	_, hdr = b.expect("GET", "/week", nil, false, 303)
	if hdr.Get("Location") != "/setup" {
		t.Fatalf("not sent to setup: %q", hdr.Get("Location"))
	}
	b.expect("GET", "/setup", nil, false, 200, "1 of 5", "5 of 5 · optional", "help of AI", "Make our first plan")
	_, hdr = b.expect("POST", "/setup", url.Values{
		"meals_breakfast": {"0"}, "meals_lunch": {"0"}, "meals_dinner": {"2"}, "meals_baking": {"1"},
		"people": {"3"}, "avoid": {"coriander, Mushrooms"}, "shopping_day": {"sunday"},
		"cuisine": {"Italian", "Danish classics"},
	}, false, 303)
	if hdr.Get("Location") != "/plans/next" {
		t.Fatalf("after setup: %q", hdr.Get("Location"))
	}
	b.expect("GET", "/plans/next", nil, false, 200, "Making your plan")
	if err := worker.RunPending(ctx); err != nil {
		t.Fatal(err)
	}
	prompt := fc.Requests[0].Messages[0].Content
	for _, want := range []string{"2 dinners and 1 baking", "People eating: 3", `"coriander"`, `"Mushrooms"`,
		"Baby portions: no", "Cuisines the family loves (lean on them, but keep variety): Italian, Danish classics"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("first plan prompt lacks %q", want)
		}
	}

	// The draft email arrives.
	ready, err := rec.Last("bea@example.test")
	if err != nil || !strings.HasPrefix(ready.Subject, "Your plan for week ") || !strings.HasSuffix(ready.Subject, " is ready.") || !strings.Contains(ready.Text, base+"/plans/next") ||
		!strings.Contains(ready.Text, "3 meals") || !strings.Contains(ready.Text, "Sunday at 08:00") {
		t.Fatalf("draft email: %+v %v", ready, err)
	}

	// Review and confirm; the list has the meals' ingredients (never water).
	b.expect("GET", "/plans/next", nil, false, 200, "Chicken traybake", "Confirm plan", "a first guess. Rate it and next week gets better.", "Confirms itself on Sunday at 08:00")
	b.expect("POST", "/plans/next/confirm", url.Values{}, false, 303)
	listHTML, _ := b.expect("GET", "/list", nil, false, 200, "Chicken thighs", "Rye flour")
	if strings.Contains(strings.ToLower(listHTML), ">water<") {
		t.Error("water on the list")
	}

	// Complete the shop: tick every item.
	ids := regexp.MustCompile(`id="item-([0-9a-f-]{36})"`).FindAllStringSubmatch(listHTML, -1)
	if len(ids) < 4 {
		t.Fatalf("%d items", len(ids))
	}
	for _, m := range ids {
		b.expect("POST", "/list/items/"+m[1]+"/toggle", url.Values{"checked": {"true"}}, true, 200, "item checked")
	}
	list, err := services.Lists.Active(ctx, domain.Scope{HouseholdID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	if done, total := list.Progress(); done != total || total != len(ids) {
		t.Errorf("shop not complete: %d of %d", done, total)
	}

	// The meal page shows the dish drawing and offers a photo (the meal
	// knows its library recipe).
	if bp, err := services.Plans.Current(ctx, domain.Scope{HouseholdID: h.ID}); err == nil && len(bp.Meals) > 0 {
		b.expect("GET", "/meals/"+bp.Meals[0].ID.String(), nil, false, 200, `class="drawing"`, "Add your photo", `action="/recipes/`)
	}

	// Household A: every id of B is 404, and A's member gets no B events.
	sub, cancel := hub.Subscribe(other.HouseholdID)
	defer cancel()
	a := newBrowser(t, srv.URL)
	sess, hash := auth.NewToken()
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (household_id, member_id, token_hash, expires_at) VALUES ($1, $2, $3, now() + interval '1 hour')`,
		other.HouseholdID, other.MemberID, hash); err != nil {
		t.Fatal(err)
	}
	a.client.Jar.SetCookies(mustURL(srv.URL), []*http.Cookie{{Name: "mp_session", Value: sess}})
	a.expect("GET", "/week", nil, false, 200) // A's own session works
	cur, _ := services.Plans.Current(ctx, domain.Scope{HouseholdID: h.ID})
	mealID := cur.Meals[0].ID.String()
	for _, c := range []struct{ method, path string }{
		{"GET", "/meals/" + mealID},
		{"POST", "/list/items/" + ids[0][1] + "/toggle"},
		{"POST", "/meals/" + mealID + "/outcome"},
		{"POST", "/households/" + h.ID.String() + "/switch"},
	} {
		if code, _, _ := a.do(c.method, c.path, url.Values{"checked": {"false"}, "rating": {"1"}}, true); code != 404 {
			t.Errorf("%s %s as household A: %d", c.method, c.path, code)
		}
	}
	// Every web route with an id, called as household A with each of B's
	// ids: never a success with content. Routes added later are covered.
	avoid, _ := services.Profiles.AvoidItems(ctx, domain.Scope{HouseholdID: h.ID})
	bIDs := []string{mealID, ids[0][1], cur.Meals[0].RecipeID.String(), avoid[0].ID.String(), h.ID.String()}
	routes, ok := web.Routes(app.Deps{Config: config.Config{BaseURL: base}, Services: services, Events: hub}).(chi.Routes)
	if !ok {
		t.Fatal("web routes are not a chi router")
	}
	checked := 0
	_ = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(route, "{id}") || method == "OPTIONS" {
			return nil
		}
		for _, id := range bIDs {
			path := strings.ReplaceAll(route, "{id}", id)
			code, body, _ := a.do(method, path, url.Values{"checked": {"false"}, "rating": {"1"}, "vote": {"-1"}, "prompt": {"x"}, "have_it": {"true"}}, true)
			if code/100 == 2 && strings.TrimSpace(body) != "" {
				t.Errorf("%s %s as household A: %d with content", method, path, code)
			}
			if code/100 == 2 || code == 404 || code == 422 || code == 405 {
				checked++
				continue
			}
			t.Errorf("%s %s as household A: unexpected %d", method, path, code)
		}
		return nil
	})
	if checked < 30 {
		t.Errorf("only %d route checks ran", checked)
	}
	select {
	case e := <-sub:
		t.Errorf("household A received an event: %+v", e)
	default:
	}
	// B's list is unchanged by A's attempts.
	list, _ = services.Lists.Active(ctx, domain.Scope{HouseholdID: h.ID})
	if done, total := list.Progress(); done != total {
		t.Errorf("A changed B's list: %d of %d", done, total)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
