package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func TestAPISchemaReducesMealPlanSchema(t *testing.T) {
	t.Parallel()
	s, err := APISchema(domain.MealPlanSchema())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	out := string(raw)
	for _, banned := range []string{`"minimum"`, `"minLength"`, `"maxItems"`, `"$schema"`, `"type":["integer","null"]`} {
		if strings.Contains(out, banned) {
			t.Errorf("reduced schema still contains %s", banned)
		}
	}
	if s["additionalProperties"] != false {
		t.Error("root additionalProperties must stay false")
	}
	meal := s["properties"].(map[string]any)["meals"].(map[string]any)["items"].(map[string]any)
	if meal["additionalProperties"] != false {
		t.Error("meal additionalProperties must stay false")
	}
	props := meal["properties"].(map[string]any)
	if _, ok := props["title"]; !ok {
		t.Error("the meal's title property was removed")
	}
	step := props["steps"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if _, ok := step["timer_min"].(map[string]any)["anyOf"]; !ok {
		t.Errorf("timer_min not rewritten to anyOf: %v", step["timer_min"])
	}
	if ing := props["ingredients"].(map[string]any); ing["minItems"] != float64(1) {
		t.Errorf("minItems 1 should be kept, got %v", ing["minItems"])
	}
	// the original is untouched
	if !strings.Contains(string(domain.MealPlanSchema()), `"minimum"`) {
		t.Error("domain schema was modified")
	}
}

func TestAPISchemaErrors(t *testing.T) {
	t.Parallel()
	if _, err := APISchema(nil); err == nil {
		t.Error("empty schema accepted")
	}
	if _, err := APISchema([]byte(`{`)); err == nil {
		t.Error("broken schema accepted")
	}
}
