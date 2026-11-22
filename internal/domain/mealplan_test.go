package domain_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(domain.MealPlanSchema()))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("meal_plan.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("meal_plan.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExampleMatchesSchemaAndStructs(t *testing.T) {
	raw, err := os.ReadFile("testdata/meal_plan_example.json")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := compileSchema(t).Validate(inst); err != nil {
		t.Fatalf("example does not match schema: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var plan domain.MealPlanJSON
	if err := dec.Decode(&plan); err != nil {
		t.Fatalf("example does not decode into MealPlanJSON: %v", err)
	}
	if got := plan.Meals[0].Steps[2]; !got.BabyPortion || got.TimerMin != nil {
		t.Fatalf("baby portion step decoded wrong: %+v", got)
	}
}

func TestSchemaRejectsShoppingList(t *testing.T) {
	inst, _ := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(`{"meals": [], "shopping_list": []}`)))
	if err := compileSchema(t).Validate(inst); err == nil {
		t.Fatal("schema accepted an extra shopping_list and an empty meals array")
	}
}
