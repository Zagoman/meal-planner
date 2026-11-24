package planner

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/domain"
)

var oneDinner = map[domain.MealType]int{domain.MealDinner: 1}

var defaultKeys = func() []string {
	var k []string
	for _, s := range domain.DefaultSections {
		k = append(k, s.Key)
	}
	return k
}()

func exampleJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../domain/testdata/meal_plan_example.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mutate(t *testing.T, raw []byte, f func(*domain.MealPlanJSON)) []byte {
	t.Helper()
	var p domain.MealPlanJSON
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	f(&p)
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func hasProblem(problems []string, sub string) bool {
	for _, p := range problems {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func TestValidateExampleIsValid(t *testing.T) {
	t.Parallel()
	p := domain.Profile{MealTargets: oneDinner, OnePot: true, BabyPortions: true}
	plan, problems := Validate(exampleJSON(t), Rules{Profile: p, SectionKeys: defaultKeys})
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(plan.Meals) != 1 || plan.Meals[0].Day == nil || *plan.Meals[0].Day != domain.Monday || plan.Meals[0].MealType != domain.MealDinner {
		t.Fatalf("decoded %+v", plan)
	}
}

func TestValidateSchemaProblems(t *testing.T) {
	t.Parallel()
	rules := Rules{Profile: domain.Profile{MealTargets: oneDinner}, SectionKeys: defaultKeys}
	for name, raw := range map[string]string{
		"not json":      `{"meals": [`,
		"empty":         `{"meals": []}`,
		"shopping list": `{"meals": [], "shopping_list": []}`,
		"bad day":       strings.Replace(string(exampleJSON(t)), `"monday"`, `"funday"`, 1),
		"zero minutes":  strings.Replace(string(exampleJSON(t)), `"minutes": 35`, `"minutes": 0`, 1),
	} {
		if _, problems := Validate([]byte(raw), rules); len(problems) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateOnePot(t *testing.T) {
	t.Parallel()
	raw := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) { p.Meals[0].OnePot = false })
	if _, problems := Validate(raw, Rules{Profile: domain.Profile{MealTargets: oneDinner}, SectionKeys: defaultKeys}); len(problems) != 0 {
		t.Fatalf("one_pot not required but rejected: %v", problems)
	}
	_, problems := Validate(raw, Rules{Profile: domain.Profile{MealTargets: oneDinner, OnePot: true}, SectionKeys: defaultKeys})
	if !hasProblem(problems, "one-pot") {
		t.Fatalf("missing one-pot problem: %v", problems)
	}
}

func TestValidateAvoidList(t *testing.T) {
	t.Parallel()
	raw := exampleJSON(t)
	for _, avoid := range []string{"Onions", "chicken", "Stock cubes", "RICE"} {
		_, problems := Validate(raw, Rules{Profile: domain.Profile{MealTargets: oneDinner, Avoid: []string{avoid}}, SectionKeys: defaultKeys})
		if !hasProblem(problems, "avoid list") {
			t.Errorf("avoid %q not detected: %v", avoid, problems)
		}
	}
	_, problems := Validate(raw, Rules{Profile: domain.Profile{MealTargets: oneDinner, Avoid: []string{"nut", "mushroom", "ice"}}, SectionKeys: defaultKeys})
	if len(problems) > 0 {
		t.Errorf("false positive: %v", problems)
	}
}

func TestValidateSections(t *testing.T) {
	t.Parallel()
	_, problems := Validate(exampleJSON(t), Rules{Profile: domain.Profile{MealTargets: oneDinner}, SectionKeys: []string{"produce", "meat_fish"}})
	if !hasProblem(problems, `unknown section "dry_goods"`) || !hasProblem(problems, `unknown section "spices"`) {
		t.Fatalf("sections not checked: %v", problems)
	}
}

func TestValidateBabyPortion(t *testing.T) {
	t.Parallel()
	baby := Rules{Profile: domain.Profile{MealTargets: oneDinner, BabyPortions: true}, SectionKeys: defaultKeys}

	noBaby := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) { p.Meals[0].Steps[2].BabyPortion = false })
	if _, problems := Validate(noBaby, baby); !hasProblem(problems, "no step has baby_portion") {
		t.Errorf("missing baby step not detected: %v", problems)
	}
	if _, problems := Validate(noBaby, Rules{Profile: domain.Profile{MealTargets: oneDinner}, SectionKeys: defaultKeys}); len(problems) > 0 {
		t.Errorf("baby step required although baby portions are off: %v", problems)
	}

	// salt before the baby step
	late := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) {
		s := p.Meals[0].Steps
		s[2], s[3] = s[3], s[2]
	})
	if _, problems := Validate(late, baby); !hasProblem(problems, "before the baby portion") {
		t.Errorf("salt before baby step not detected: %v", problems)
	}

	for _, text := range []string{"Stir in the grated parmesan.", "Pour in the chicken stock.", "Add the CHEESE"} {
		raw := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) { p.Meals[0].Steps[0].Text = text })
		if _, problems := Validate(raw, baby); !hasProblem(problems, "before the baby portion") {
			t.Errorf("%q before baby step not detected: %v", text, problems)
		}
	}
	unsalted := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) { p.Meals[0].Steps[0].Text = "Melt unsalted butter." })
	if _, problems := Validate(unsalted, baby); len(problems) > 0 {
		t.Errorf("unsalted butter flagged: %v", problems)
	}

	two := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) { p.Meals[0].Steps[3].BabyPortion = true })
	if _, problems := Validate(two, baby); !hasProblem(problems, "exactly one step") {
		t.Errorf("two baby steps not detected: %v", problems)
	}
}

func TestValidateDaysAndSwap(t *testing.T) {
	t.Parallel()
	two := map[domain.MealType]int{domain.MealDinner: 2}
	dup := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) { p.Meals = append(p.Meals, p.Meals[0]) })
	if _, problems := Validate(dup, Rules{Profile: domain.Profile{MealTargets: two}, SectionKeys: defaultKeys}); !hasProblem(problems, "already suggested for monday") {
		t.Errorf("duplicate day not detected: %v", problems)
	}
	noDay := mutate(t, dup, func(p *domain.MealPlanJSON) { p.Meals[1].Day = nil })
	if _, problems := Validate(noDay, Rules{Profile: domain.Profile{MealTargets: two}, SectionKeys: defaultKeys}); len(problems) > 0 {
		t.Errorf("a meal without a day was rejected: %v", problems)
	}
	swap := func(mt domain.MealType, d domain.Weekday) Rules {
		return Rules{SectionKeys: defaultKeys, Swap: &SwapRule{MealType: mt, Day: d}}
	}
	if _, problems := Validate(exampleJSON(t), swap(domain.MealDinner, domain.Tuesday)); !hasProblem(problems, "day must be tuesday") {
		t.Errorf("wrong swap day not detected: %v", problems)
	}
	if _, problems := Validate(exampleJSON(t), swap(domain.MealLunch, "")); !hasProblem(problems, "must be a lunch") {
		t.Errorf("wrong swap meal type not detected: %v", problems)
	}
	if _, problems := Validate(dup, swap(domain.MealDinner, domain.Monday)); !hasProblem(problems, "exactly one meal") {
		t.Errorf("two meals in a swap not detected: %v", problems)
	}
}

func TestValidateMealTargets(t *testing.T) {
	t.Parallel()
	week := mutate(t, exampleJSON(t), func(p *domain.MealPlanJSON) {
		bread := p.Meals[0]
		bread.MealType, bread.Day, bread.Title = domain.MealBaking, nil, "Rye bread"
		lunch := p.Meals[0]
		tue := domain.Tuesday
		lunch.MealType, lunch.Day, lunch.Title = domain.MealLunch, &tue, "Rice salad"
		p.Meals = append(p.Meals, bread, lunch)
	})
	ok := map[domain.MealType]int{domain.MealDinner: 1, domain.MealLunch: 1, domain.MealBaking: 1}
	if _, problems := Validate(week, Rules{Profile: domain.Profile{MealTargets: ok}, SectionKeys: defaultKeys}); len(problems) > 0 {
		t.Fatalf("matching week rejected: %v", problems)
	}
	wrong := map[domain.MealType]int{domain.MealDinner: 2, domain.MealBreakfast: 1, domain.MealBaking: 1}
	_, problems := Validate(week, Rules{Profile: domain.Profile{MealTargets: wrong}, SectionKeys: defaultKeys})
	for _, want := range []string{"exactly 2 dinner meals, not 1", "exactly 1 breakfast meals, not 0", "exactly 0 lunch meals, not 1"} {
		if !hasProblem(problems, want) {
			t.Errorf("missing %q in %v", want, problems)
		}
	}
	if hasProblem(problems, "baking") {
		t.Errorf("baking matched but was flagged: %v", problems)
	}
}
