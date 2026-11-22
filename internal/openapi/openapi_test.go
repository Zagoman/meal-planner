package openapi

import (
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// The routes the build spec requires. Adding a route is fine; removing one
// breaks the contract other packages build against.
var required = map[string][]string{
	"/plans":                             {"post"},
	"/plans/current":                     {"get"},
	"/plans/{plan_id}/meals/{day}/swap":  {"post"},
	"/meals/{meal_id}/substitute":        {"post"},
	"/list/active":                       {"get"},
	"/list/stream":                       {"get"},
	"/list/items":                        {"post"},
	"/list/items/{item_id}":              {"patch", "delete"},
	"/profile":                           {"get", "put"},
	"/sections":                          {"get", "put"},
	"/pantry":                            {"get", "put"},
	"/feedback":                          {"post"},
	"/households":                        {"post"},
	"/households/{household_id}/invites": {"post"},
	"/invites/{token}/accept":            {"post"},
	"/admin/usage":                       {"get"},
}

func TestSpecHasRequiredRoutes(t *testing.T) {
	var doc struct {
		OpenAPI string                            `yaml:"openapi"`
		Paths   map[string]map[string]interface{} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(Spec, &doc); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Fatal("missing openapi version")
	}
	var missing []string
	for path, methods := range required {
		for _, m := range methods {
			if _, ok := doc.Paths[path][m]; !ok {
				missing = append(missing, m+" "+path)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("missing operations: %v", missing)
	}
}
