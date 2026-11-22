package api_test

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/openapi"
)

// isoCase is one operation of openapi.yaml. bWant is what household B gets
// when it aims the request at household A's IDs; aWant is what A gets for
// the same request on its own data (so a 404 for B is not just a broken
// route).
type isoCase struct {
	method, route string // route exactly as in openapi.yaml
	body          string // may use {meal_id} etc.
	bWant, aWant  int
}

var isoCases = []isoCase{
	{"POST", "/plans", `{"prompt":"b wants a plan"}`, 201, 201},
	{"GET", "/plans/current", ``, 404, 200},
	{"POST", "/plans/{plan_id}/meals/{day}/swap", `{"prompt":"x"}`, 404, 200},
	{"GET", "/meals/{meal_id}", ``, 404, 200},
	{"POST", "/meals/{meal_id}/substitute", `{"from":"cream","to":"milk"}`, 404, 200},
	{"GET", "/list/active", ``, 404, 200},
	{"GET", "/list/stream", ``, 200, 200},
	{"POST", "/list/items", `{"name":"b soap"}`, 404, 201}, // B has no active plan
	{"PATCH", "/list/items/{item_id}", `{"checked":true}`, 404, 200},
	{"DELETE", "/list/items/{item_id}", ``, 404, 204},
	{"GET", "/profile", ``, 200, 200},
	{"PUT", "/profile", `{"dinners_per_week":3,"servings":2,"spice_level":"hot","one_pot":true,"protein_mix":{"fish":3},"avoid":["b-avoid"],"shop":"B shop","shopping_day":"monday","baby_portions":false,"language":"en"}`, 200, 200},
	{"GET", "/sections", ``, 200, 200},
	{"PUT", "/sections", `[{"key":"b_only","name":"B only"}]`, 200, 200},
	{"GET", "/pantry", ``, 200, 200},
	{"PUT", "/pantry", `["b salt"]`, 200, 200},
	{"POST", "/feedback", `{"meal_id":"{meal_id}","rating":5,"note":"b"}`, 404, 201},
	{"POST", "/households", `{"name":"B2","admin_email":"b2@example.test"}`, 403, 403}, // neither is server admin
	{"POST", "/households/{household_id}/invites", `{"role":"admin"}`, 404, 201},
	{"POST", "/invites/{token}/accept", ``, 404, 404}, // a guessed token; real invites are in TestInvite*
	{"GET", "/admin/usage", ``, 403, 403},
}

func (e *env) fill(s string) string {
	return strings.NewReplacer(
		"{plan_id}", e.aPlanID.String(),
		"{day}", "monday",
		"{meal_id}", e.aMealID.String(),
		"{item_id}", e.aItem.String(),
		"{household_id}", e.a.scope.HouseholdID.String(),
		"{token}", "guessed-token-guessed-token-guessed-token00",
	).Replace(s)
}

// run sends c as p. The stream route is read for a short while, during
// which household A's list keeps changing.
func (e *env) run(c isoCase, p party) (int, string) {
	path := "/api" + e.fill(c.route)
	if c.route != "/list/stream" {
		return e.do(p.token, c.method, path, e.fill(c.body))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			e.hub.Publish(e.a.scope.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: e.aItem})
			e.hub.Publish(e.a.scope.HouseholdID, events.Event{Kind: events.ItemDeleted, ItemID: e.aItem})
			e.hub.Publish(e.a.scope.HouseholdID, events.Event{Kind: events.ListRebuilt})
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return e.doCtx(ctx, p.token, c.method, path, "")
}

// TestHouseholdIsolation is the hard rule: for every route in openapi.yaml,
// household B can neither read nor change household A's data.
func TestHouseholdIsolation(t *testing.T) {
	t.Parallel()
	for _, c := range isoCases {
		t.Run(c.method+" "+c.route, func(t *testing.T) {
			t.Parallel()
			t.Run("B cannot touch A", func(t *testing.T) {
				t.Parallel()
				e := newEnv(t)
				code, body := e.run(c, e.b)
				if code != c.bWant {
					t.Errorf("B got %d, want %d: %s", code, c.bWant, body)
				}
				if strings.Contains(strings.ToLower(body), marker) || strings.Contains(body, e.aItem.String()) ||
					strings.Contains(body, e.aMealID.String()) || strings.Contains(body, e.aPlanID.String()) {
					t.Errorf("B's response leaks A's data: %s", body)
				}
				if c.route == "/list/stream" && strings.Contains(body, "event:") {
					t.Errorf("B's stream received events: %s", body)
				}
				e.assertAUnchanged()
			})
			t.Run("A on its own data", func(t *testing.T) {
				t.Parallel()
				e := newEnv(t)
				if code, body := e.run(c, e.a); code != c.aWant {
					t.Errorf("A got %d, want %d: %s", code, c.aWant, body)
				}
			})
		})
	}
}

// TestIsolationCoversEveryRoute fails when openapi.yaml gains a route that
// TestHouseholdIsolation does not exercise.
func TestIsolationCoversEveryRoute(t *testing.T) {
	t.Parallel()
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapi.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range isoCases {
		have[c.method+" "+c.route] = true
	}
	var missing []string
	n := 0
	for path, ops := range spec.Paths {
		for m := range ops {
			switch m {
			case "get", "post", "put", "patch", "delete":
				n++
				if k := strings.ToUpper(m) + " " + path; !have[k] {
					missing = append(missing, k)
				}
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routes without an isolation case: %v", missing)
	}
	if n != len(isoCases) {
		t.Fatalf("openapi has %d operations, isoCases %d", n, len(isoCases))
	}
}

// TestUnknownIDsAre404 checks malformed and random IDs are 404 like another
// household's (no 400/422 that would tell IDs apart).
func TestUnknownIDsAre404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, id := range []string{"not-a-uuid", uuid.NewString()} {
		for _, c := range []struct{ method, path, body string }{
			{"GET", "/api/meals/" + id, ""},
			{"POST", "/api/meals/" + id + "/substitute", `{"from":"a","to":"b"}`},
			{"POST", "/api/plans/" + id + "/meals/monday/swap", `{"prompt":""}`},
			{"PATCH", "/api/list/items/" + id, `{"checked":true}`},
			{"DELETE", "/api/list/items/" + id, ""},
			{"POST", "/api/households/" + id + "/invites", ""},
		} {
			if code, body := e.do(e.a.token, c.method, c.path, c.body); code != http.StatusNotFound {
				t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
			}
		}
	}
}
