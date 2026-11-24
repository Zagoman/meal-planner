package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/server"
	"github.com/Zagoman/meal-planner/internal/service"
)

func TestBearerAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	revoked := e.newParty(e.a.scope)
	toks, err := e.auth.APITokens(context.Background(), e.a.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.auth.RevokeAPIToken(context.Background(), e.a.scope, toks[0].ID); err != nil { // newest first
		t.Fatal(err)
	}
	cases := map[string]string{
		"no header":     "",
		"wrong scheme":  "Basic " + e.a.token,
		"empty bearer":  "Bearer ",
		"unknown token": "Bearer nope",
		"revoked token": "Bearer " + revoked.token,
	}
	for name, header := range cases {
		req := httptest.NewRequest("GET", "/api/profile", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"unauthorized"`) ||
			rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d %q %s", name, rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body.String())
		}
	}
	if code, _ := e.do(e.a.token, "GET", "/api/profile", ""); code != http.StatusOK {
		t.Errorf("valid token: %d", code)
	}
	// Lower-case scheme is fine (RFC 7235).
	req := httptest.NewRequest("GET", "/api/profile", nil)
	req.Header.Set("Authorization", "bearer "+e.a.token)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("lower-case bearer: %d", rec.Code)
	}
	// Unknown routes need auth too and are JSON 404s.
	if code, body := e.do(e.a.token, "GET", "/api/nope", ""); code != 404 || !strings.Contains(body, "not_found") {
		t.Errorf("unknown route: %d %s", code, body)
	}
	if code, _ := e.do("", "GET", "/api/nope", ""); code != 401 {
		t.Errorf("unknown route without token: %d", code)
	}
}

func TestMountedInServer(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	h := server.New(app.Deps{Pool: e.pool, Events: e.hub, Services: service.Services{
		Auth: e.auth, Households: e.hh, Profiles: e.hh, Feedback: e.hh, Plans: e.fake, Lists: e.fake,
	}})
	req := httptest.NewRequest("GET", "/api/plans/current", nil)
	req.Header.Set("Authorization", "Bearer "+e.a.token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "marker prompt") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRoles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()

	// A plain member of A cannot create invites.
	m, err := e.auth.Register(ctx, "member@example.test", "Mem", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: e.a.scope.HouseholdID, MemberID: m.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	mem := e.newParty(domain.Scope{HouseholdID: e.a.scope.HouseholdID, MemberID: m.ID, Role: domain.RoleMember})
	if code, body := e.do(mem.token, "POST", "/api/households/"+e.a.scope.HouseholdID.String()+"/invites", ""); code != 403 {
		t.Errorf("member invite: %d %s", code, body)
	}
	// ...but can use the household normally.
	if code, _ := e.do(mem.token, "PATCH", "/api/list/items/"+e.aItem.String(), `{"checked":true}`); code != 200 {
		t.Errorf("member tick: %d", code)
	}

	// The ADMIN_EMAIL member is server admin.
	root, err := e.auth.Register(ctx, adminEmail, "Root", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: e.b.scope.HouseholdID, MemberID: root.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	rootTok, err := e.auth.CreateAPIToken(ctx, domain.Scope{HouseholdID: e.b.scope.HouseholdID, MemberID: root.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := e.do(rootTok, "POST", "/api/households", `{"name":"Cottage","admin_email":"Cottage@Example.test"}`)
	if code != 201 {
		t.Fatalf("server admin create household: %d %s", code, body)
	}
	var hh domain.Household
	_ = json.Unmarshal([]byte(body), &hh)
	secs, err := e.q.ListSections(ctx, hh.ID)
	if err != nil || len(secs) != len(domain.DefaultSections) {
		t.Errorf("new household sections: %d %v", len(secs), err)
	}
	if _, err := e.q.GetProfile(ctx, hh.ID); err != nil {
		t.Errorf("new household profile: %v", err)
	}
	cm, err := e.q.GetMemberByEmail(ctx, "cottage@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if hm, err := e.q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: hh.ID, MemberID: cm.ID}); err != nil || hm.Role != "admin" {
		t.Errorf("cottage admin membership: %+v %v", hm, err)
	}
	if code, body := e.do(rootTok, "POST", "/api/households", `{"name":"","admin_email":"x"}`); code != 422 {
		t.Errorf("invalid household: %d %s", code, body)
	}
	code, body = e.do(rootTok, "GET", "/api/admin/usage?since=2020-01-01", "")
	if code != 200 || !strings.Contains(body, "Cottage") || !strings.Contains(body, "Amarker") {
		t.Errorf("usage: %d %s", code, body)
	}
	if code, _ := e.do(rootTok, "GET", "/api/admin/usage?since=yesterday", ""); code != 422 {
		t.Errorf("bad since: %d", code)
	}
}

func TestInviteFlow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	code, body := e.do(e.a.token, "POST", "/api/households/"+e.a.scope.HouseholdID.String()+"/invites", "")
	if code != 201 {
		t.Fatalf("create invite: %d %s", code, body)
	}
	var inv domain.Invite
	if err := json.Unmarshal([]byte(body), &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Role != domain.RoleMember || inv.URL != baseURL+"/invite/"+inv.Token || len(inv.Token) != 43 ||
		inv.ExpiresAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("invite: %+v", inv)
	}
	// B's admin joins A with the token, as a member.
	code, body = e.do(e.b.token, "POST", "/api/invites/"+inv.Token+"/accept", "")
	if code != 200 || !strings.Contains(body, `"role":"member"`) || !strings.Contains(body, e.a.scope.HouseholdID.String()) {
		t.Fatalf("accept: %d %s", code, body)
	}
	if _, err := e.q.GetMembership(ctx, db.GetMembershipParams{HouseholdID: e.a.scope.HouseholdID, MemberID: e.b.scope.MemberID}); err != nil {
		t.Fatalf("not a member after accept: %v", err)
	}
	// Single use.
	if code, body := e.do(e.b.token, "POST", "/api/invites/"+inv.Token+"/accept", ""); code != 409 {
		t.Errorf("reuse: %d %s", code, body)
	}
	// Joining A does not widen B's token: it still sees only B.
	if code, body := e.do(e.b.token, "GET", "/api/meals/"+e.aMealID.String(), ""); code != 404 {
		t.Errorf("B token after joining A: %d %s", code, body)
	}
	// Expired.
	code, body = e.do(e.a.token, "POST", "/api/households/"+e.a.scope.HouseholdID.String()+"/invites", `{"role":"admin"}`)
	if code != 201 {
		t.Fatal(code, body)
	}
	_ = json.Unmarshal([]byte(body), &inv)
	if _, err := e.pool.Exec(ctx, "UPDATE invites SET expires_at = now() - interval '1 second' WHERE id = $1", inv.ID); err != nil {
		t.Fatal(err)
	}
	c := e.newParty(newScope(t, e, "c@example.test"))
	if code, body := e.do(c.token, "POST", "/api/invites/"+inv.Token+"/accept", ""); code != 404 {
		t.Errorf("expired: %d %s", code, body)
	}
	if code, body := e.do(e.a.token, "POST", "/api/households/"+e.a.scope.HouseholdID.String()+"/invites", `{"role":"owner"}`); code != 422 {
		t.Errorf("bad role: %d %s", code, body)
	}
}

// newScope makes a member alone in a new household (via the real service).
func newScope(t *testing.T, e *env, email string) domain.Scope {
	t.Helper()
	ctx := context.Background()
	m, err := e.auth.Register(ctx, email, "", "password123")
	if err != nil {
		t.Fatal(err)
	}
	h, err := e.q.CreateHousehold(ctx, "C")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.AddHouseholdMember(ctx, db.AddHouseholdMemberParams{HouseholdID: h.ID, MemberID: m.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	return domain.Scope{HouseholdID: h.ID, MemberID: m.ID, Role: domain.RoleAdmin}
}

func TestValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	long := strings.Repeat("a", 2001)
	cases := []struct{ method, path, body string }{
		{"POST", "/api/plans", `{}`},
		{"POST", "/api/plans", `{"prompt":"` + long + `"}`},
		{"POST", "/api/plans", `{"prompt":"x","extra":1}`},
		{"POST", "/api/plans", `not json`},
		{"POST", "/api/plans/" + e.aPlanID.String() + "/meals/funday/swap", `{"prompt":""}`},
		{"POST", "/api/meals/" + e.aMealID.String() + "/substitute", `{"from":"","to":"x"}`},
		{"POST", "/api/list/items", `{"name":"  "}`},
		{"PATCH", "/api/list/items/" + e.aItem.String(), `{}`},
		{"PUT", "/api/profile", `{"meal_targets":{"dinner":15}}`},
		{"PUT", "/api/profile", `{"dinners_per_week":4}`},
		{"PUT", "/api/sections", `[{"key":"Bad Key","name":"x"}]`},
		{"PUT", "/api/sections", `[{"key":"pantry_check","name":"x"}]`},
		{"PUT", "/api/sections", `[{"key":"a","name":"x"},{"key":"a","name":"y"}]`},
		{"PUT", "/api/sections", `[]`},
		{"PUT", "/api/pantry", `"salt"`},
		{"POST", "/api/feedback", `{"meal_id":"` + e.aMealID.String() + `","rating":6}`},
		{"POST", "/api/feedback", `{"rating":3}`},
	}
	for _, c := range cases {
		code, body := e.do(e.a.token, c.method, c.path, c.body)
		if code != 422 || !strings.Contains(body, "invalid_input") {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, code, body)
		}
	}
	e.assertAUnchanged()
}

func TestProfileRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	code, body := e.do(e.a.token, "PUT", "/api/sections", `[{"key":"dairy_eggs","name":"Dairy","sort_order":99},{"key":"produce","name":" Veg "}]`)
	if code != 200 || body != `[{"key":"dairy_eggs","name":"Dairy","sort_order":10},{"key":"produce","name":"Veg","sort_order":20}]`+"\n" {
		t.Errorf("sections: %d %s", code, body)
	}
	code, body = e.do(e.a.token, "PUT", "/api/pantry", `["Olive Oil", "  tomatoes ", "Tomato", ""]`)
	if code != 200 || !strings.Contains(body, `"normalized_name":"olive oil"`) || strings.Count(body, `"normalized_name":"tomato"`) != 1 {
		t.Errorf("pantry: %d %s", code, body)
	}
	code, body = e.do(e.a.token, "PUT", "/api/profile",
		`{"meal_targets":{"breakfast":7,"dinner":5},"people":4,"spice_level":"mild","one_pot":false,"protein_mix":{"chicken":2},"avoid":[" peanuts ","Peanuts"],"shop":"Netto","shopping_day":"friday","baby_portions":true,"language":""}`)
	if code != 200 || !strings.Contains(body, `"avoid":["peanuts"]`) || !strings.Contains(body, `"language":"en"`) ||
		!strings.Contains(body, `"meal_targets":{"baking":0,"breakfast":7,"dinner":5,"lunch":0}`) || !strings.Contains(body, `"auto_confirm_time":"08:00"`) {
		t.Errorf("profile: %d %s", code, body)
	}
	if _, body := e.do(e.a.token, "GET", "/api/profile", ""); !strings.Contains(body, `"shop":"Netto"`) {
		t.Errorf("profile get: %s", body)
	}
}

func TestListStream(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	srv := httptest.NewServer(e.handler)
	t.Cleanup(srv.Close)

	open := func(p party) (*bufio.Scanner, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/list/stream", nil)
		req.Header.Set("Authorization", "Bearer "+p.token)
		resp, err := srv.Client().Do(req) //nolint:bodyclose // closed by the returned func
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		return bufio.NewScanner(resp.Body), func() { cancel(); _ = resp.Body.Close() }
	}
	lines := make(chan string, 100)
	sa, closeA := open(e.a)
	defer closeA()
	go func() {
		for sa.Scan() {
			lines <- sa.Text()
		}
		close(lines)
	}()
	next := func(prefix string) string {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed waiting for %q", prefix)
				}
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case <-timeout:
				t.Fatalf("timeout waiting for %q", prefix)
			}
		}
	}
	next("event: list")
	if l := next("data: "); !strings.Contains(l, "Marker onions") {
		t.Fatalf("initial list: %s", l)
	}
	next(": keep-alive")

	// B also streams; A's changes must not reach it.
	sb, closeB := open(e.b)
	defer closeB()
	bLines := make(chan string, 100)
	go func() {
		for sb.Scan() {
			bLines <- sb.Text()
		}
		close(bLines)
	}()

	code, body := e.do(e.a.token, "POST", "/api/list/items", `{"name":"Marker soap"}`)
	if code != 201 {
		t.Fatal(code, body)
	}
	var item domain.ListItem
	_ = json.Unmarshal([]byte(body), &item)
	next("event: item")
	if l := next("data: "); !strings.Contains(l, item.ID.String()) || !strings.Contains(l, "Marker soap") {
		t.Fatalf("item event: %s", l)
	}
	if code, _ := e.do(e.a.token, "DELETE", "/api/list/items/"+item.ID.String(), ""); code != 204 {
		t.Fatal(code)
	}
	next("event: deleted")
	if l := next("data: "); l != `data: {"id":"`+item.ID.String()+`"}` {
		t.Fatalf("deleted event: %s", l)
	}
	e.hub.Publish(e.a.scope.HouseholdID, events.Event{Kind: events.ListRebuilt})
	next("event: list")

	closeB()
	for l := range bLines {
		if strings.HasPrefix(l, "event:") || strings.Contains(strings.ToLower(l), marker) {
			t.Errorf("B's stream got A's data: %s", l)
		}
	}
}
