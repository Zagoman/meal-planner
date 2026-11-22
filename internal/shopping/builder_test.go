package shopping

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Onions, chopped":         "onion",
		"  Red   ONIONS ":         "red onion",
		"chicken thighs":          "chicken thigh",
		"tomatoes (tinned)":       "tomato",
		"cherry tomatoes":         "cherry tomato",
		"Long-grain rice":         "long grain rice",
		"berries":                 "berry",
		"radishes":                "radish",
		"cloves":                  "clove",
		"hummus":                  "hummus",
		"couscous":                "couscous",
		"asparagus":               "asparagus",
		"peas":                    "pea",
		"cheeses":                 "cheese",
		"løg":                     "løg",
		"Crème fraîche 18%":       "crème fraîche 18",
		"stock cube":              "stock cube",
		"":                        "",
		"!!!":                     "",
		"gas":                     "gas",
		"chickpeas, drained":      "chickpea",
		"boxes":                   "box",
		"fresh basil (a handful)": "fresh basil",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
		if got := Normalize(Normalize(in)); got != Normalize(in) {
			t.Errorf("Normalize not idempotent for %q: %q", in, got)
		}
	}
}

func TestContainsTerm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, term string
		want       bool
	}{
		{"chestnut mushrooms", "mushroom", true},
		{"mushroom", "Mushrooms", true},
		{"mushroom soup", "mushrooms", true},
		{"peanut", "nut", false},
		{"pine nuts", "nuts", true},
		{"pork belly", "pork", true},
		{"spring onion", "onion", true},
		{"onion", "", false},
		{"coriander", "cilantro", false},
	}
	for _, c := range cases {
		if got := ContainsTerm(c.name, c.term); got != c.want {
			t.Errorf("ContainsTerm(%q, %q) = %v, want %v", c.name, c.term, got, c.want)
		}
	}
}

func TestFormatAndConvert(t *testing.T) {
	t.Parallel()
	cases := []struct {
		add  [][2]any // qty, unit
		want string
	}{
		{[][2]any{{500.0, "g"}, {0.25, "kg"}}, "750 g"},
		{[][2]any{{600.0, "g"}, {0.9, "kg"}}, "1.5 kg"},
		{[][2]any{{2.0, "dl"}, {300.0, "ml"}}, "500 ml"},
		{[][2]any{{0.5, "l"}, {5.0, "dl"}, {250.0, "ml"}}, "1.25 l"},
		{[][2]any{{1.0, "tbsp"}, {3.0, "tsp"}}, "2 tbsp"},
		{[][2]any{{1.0, "tbsp"}, {1.0, "tsp"}}, "4 tsp"},
		{[][2]any{{2.0, "pcs"}, {1.0, ""}}, "3"},
		{[][2]any{{2.0, "cloves"}, {1.0, "clove"}}, "3 cloves"},
		{[][2]any{{1.0, "can"}}, "1 can"},
		{[][2]any{{200.0, "g"}, {2.0, "pcs"}}, "200 g + 2"},
		{[][2]any{{0.0, "pcs"}}, ""},
		{[][2]any{{1.0, "pinch"}, {1.0, "pinches"}}, "2 pinches"},
		{[][2]any{{1.0, "Kg"}, {1.0, "G"}}, "1 kg"},
	}
	for _, c := range cases {
		a := amounts{}
		for _, x := range c.add {
			a.add(x[0].(float64), x[1].(string))
		}
		if got := a.String(); got != c.want {
			t.Errorf("%v: got %q, want %q", c.add, got, c.want)
		}
	}
}

func ing(name string, qty float64, unit, section string) domain.Ingredient {
	return domain.Ingredient{Name: name, NormalizedName: Normalize(name), Qty: qty, Unit: unit, Section: section}
}

func TestBuildMergesAndUsesBuyingUnits(t *testing.T) {
	t.Parallel()
	items := Build([]domain.Ingredient{
		ing("onions, chopped", 2, "pcs", "produce"),
		ing("onion", 1, "", "produce"),
		ing("chicken thighs", 500, "g", "meat_fish"),
		ing("chicken thigh", 0.4, "kg", "meat_fish"),
		ing("rice", 200, "g", "dry_goods"),
		ing("garlic", 3, "cloves", "produce"),
		ing("milk", 3, "dl", "dairy_eggs"),
		ing("paprika", 1, "tsp", "spices"),
		ing("paprika", 1, "tbsp", "spices"),
	}, domain.DefaultSections, nil)

	want := []Item{
		{Name: "Garlic", NormalizedName: "garlic", QtyText: "1 head", NeedText: "need 3 cloves", Section: "produce"},
		{Name: "Onions", NormalizedName: "onion", QtyText: "1 net", NeedText: "need 3", Section: "produce"},
		{Name: "Chicken thighs", NormalizedName: "chicken thigh", QtyText: "2 packs (500 g)", NeedText: "need 900 g", Section: "meat_fish"},
		{Name: "Milk", NormalizedName: "milk", QtyText: "1 carton (1 l)", NeedText: "need 300 ml", Section: "dairy_eggs"},
		{Name: "Rice", NormalizedName: "rice", QtyText: "1 bag (1 kg)", NeedText: "need 200 g", Section: "dry_goods"},
		{Name: "Paprika", NormalizedName: "paprika", QtyText: "4 tsp", NeedText: "", Section: "spices"},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Errorf("item %d:\n got  %+v\n want %+v", i, items[i], want[i])
		}
	}
}

func TestBuildPantrySectionsAndFallback(t *testing.T) {
	t.Parallel()
	sections := []domain.Section{
		{Key: "zz_last", Name: "Other stuff", SortOrder: 99},
		{Key: "produce", Name: "Veg", SortOrder: 1},
		{Key: "dairy_eggs", Name: "Dairy", SortOrder: 2},
	}
	items := Build([]domain.Ingredient{
		ing("olive oil", 2, "tbsp", "tins_jars"), // pantry, unknown section
		ing("salt", 0, "", "spices"),             // pantry
		ing("cheddar", 100, "g", "dairy_eggs"),
		ing("carrots", 3, "pcs", "produce"),
		ing("tofu", 400, "g", "nonexistent"), // falls back to last section
	}, sections, []string{"Olive Oil", "salt"})

	gotOrder := []string{}
	for _, it := range items {
		gotOrder = append(gotOrder, it.Section+":"+it.NormalizedName)
	}
	want := []string{
		"produce:carrot", "dairy_eggs:cheddar", "zz_last:tofu",
		"pantry_check:olive oil", "pantry_check:salt",
	}
	if len(gotOrder) != len(want) {
		t.Fatalf("got %v", gotOrder)
	}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Fatalf("order: got %v, want %v", gotOrder, want)
		}
	}
	if items[0].QtyText != "1 bag" || items[0].NeedText != "need 3" {
		t.Errorf("carrots: %+v", items[0])
	}
	if items[4].QtyText != "" || items[4].NeedText != "" {
		t.Errorf("salt to taste should have no quantity: %+v", items[4])
	}
}

func TestBuildMixedUnitsFallBackToSum(t *testing.T) {
	t.Parallel()
	items := Build([]domain.Ingredient{
		ing("onion", 2, "pcs", "produce"),
		ing("onion", 100, "g", "produce"),
	}, domain.DefaultSections, nil)
	if len(items) != 1 || items[0].QtyText != "100 g + 2" || items[0].NeedText != "" || items[0].Name != "Onion" {
		t.Fatalf("got %+v", items)
	}
}

func TestBuildUniqueKeysAndSkipsEmptyNames(t *testing.T) {
	t.Parallel()
	items := Build([]domain.Ingredient{
		{Name: "Tomatoes", NormalizedName: "tomato", Qty: 2, Unit: "pcs", Section: "produce"},
		{Name: "tomato", NormalizedName: "Tomatoes", Qty: 1, Unit: "pcs", Section: "produce"},
		{Name: "???", NormalizedName: "", Qty: 1, Unit: "pcs", Section: "produce"},
	}, domain.DefaultSections, nil)
	if len(items) != 1 || items[0].QtyText != "3" {
		t.Fatalf("got %+v", items)
	}
}

func TestGroup(t *testing.T) {
	t.Parallel()
	planID := uuid.New()
	items := []domain.ListItem{
		{Name: "salt", Section: domain.PantryCheckSection},
		{Name: "Milk", Section: "dairy_eggs", Checked: true},
		{Name: "Bananas", Section: "produce"},
		{Name: "apples", Section: "produce"},
		{Name: "Mystery", Section: "gone_section"},
	}
	l := Group(planID, items, domain.DefaultSections)
	if l.PlanID != planID {
		t.Fatal("plan id")
	}
	var keys []string
	for _, s := range l.Sections {
		keys = append(keys, s.Key)
	}
	want := []string{"produce", "dairy_eggs", "gone_section", domain.PantryCheckSection}
	if len(keys) != len(want) {
		t.Fatalf("keys %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys %v, want %v", keys, want)
		}
	}
	if l.Sections[0].Name != "Fruit & veg" || l.Sections[0].Items[0].Name != "apples" {
		t.Errorf("produce section: %+v", l.Sections[0])
	}
	if l.Sections[2].Name != "Gone section" || l.Sections[3].Name != PantryCheckName {
		t.Errorf("names: %q %q", l.Sections[2].Name, l.Sections[3].Name)
	}
	if empty := Group(planID, nil, domain.DefaultSections); empty.Sections == nil {
		t.Error("empty list must have a non-nil sections slice (JSON [])")
	}
}
