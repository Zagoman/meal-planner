package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

// Rules are what a Claude answer is checked against.
type Rules struct {
	Profile     domain.Profile
	SectionKeys []string
	// Swap, when set, means exactly one meal of that type (and day, when
	// the swap has one) instead of a whole week matching the meal targets.
	Swap *SwapRule
	// Replace, when set, means exactly these meal types (as a multiset):
	// the replacements for several meals at once.
	Replace []domain.MealType
	// AnyCount skips the meal-target check (Ask Claude may change counts);
	// each type still holds at most domain.MaxMealsPerType meals.
	AnyCount bool
}

// SwapRule is the one meal a swap must return.
type SwapRule struct {
	MealType domain.MealType
	Day      domain.Weekday // "" for any
}

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(domain.MealPlanSchema()))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource("meal_plan.schema.json", doc); err != nil {
			schemaErr = err
			return
		}
		schema, schemaErr = c.Compile("meal_plan.schema.json")
	})
	return schema, schemaErr
}

// saltyStep matches step text that adds salt, stock or cheese. ASCII word
// boundaries keep "unsalted butter" from matching.
var saltyStep = regexp.MustCompile(`(?i)\b(salt|salted|salty|stock|stocks|bouillon|broth|cheese|cheeses|parmesan|parmigiano|pecorino|cheddar|feta|mozzarella|halloumi|gruyere|soy sauce|fish sauce)\b`)

// Validate parses a Claude answer and returns every problem found, phrased so
// they can be sent back to Claude in a retry. No problems means the plan can
// be saved.
func Validate(raw []byte, r Rules) (domain.MealPlanJSON, []string) {
	var plan domain.MealPlanJSON
	s, err := compiledSchema()
	if err != nil {
		return plan, []string{"internal: meal plan schema does not compile: " + err.Error()}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return plan, []string{"the answer is not valid JSON: " + err.Error()}
	}
	if err := s.Validate(inst); err != nil {
		return plan, []string{"the answer does not match the schema: " + truncate(err.Error(), 1500)}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return plan, []string{"the answer does not match the schema: " + err.Error()}
	}

	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if r.Swap != nil {
		switch {
		case len(plan.Meals) != 1:
			add("return exactly one meal (a %s), not %d", r.Swap.MealType, len(plan.Meals))
		case plan.Meals[0].MealType != r.Swap.MealType:
			add("the meal must be a %s, not a %s", r.Swap.MealType, plan.Meals[0].MealType)
		case r.Swap.Day != "" && (plan.Meals[0].Day == nil || *plan.Meals[0].Day != r.Swap.Day):
			add("the meal's day must be %s", r.Swap.Day)
		}
	} else if r.Replace != nil {
		want := map[domain.MealType]int{}
		for _, t := range r.Replace {
			want[t]++
		}
		problems = append(problems, countProblems(plan.Meals, want, "the answer")...)
	} else if r.AnyCount {
		got := map[domain.MealType]int{}
		for _, m := range plan.Meals {
			got[m.MealType]++
		}
		for _, t := range domain.PlannedMealTypes {
			if got[t] > domain.MaxMealsPerType {
				add("at most %d %s meals, not %d", domain.MaxMealsPerType, t, got[t])
			}
		}
	} else {
		problems = append(problems, targetProblems(plan.Meals, r.Profile.MealTargets)...)
	}

	sections := map[string]bool{}
	for _, k := range r.SectionKeys {
		sections[k] = true
	}
	seenDay := map[domain.MealType]map[domain.Weekday]bool{}
	for _, m := range plan.Meals {
		name := mealName(m)
		// Two meals of one type may share a day only when the type has
		// more meals than days.
		if m.Day != nil && r.Swap == nil && r.Replace == nil && r.Profile.MealTargets[m.MealType] <= len(domain.Weekdays) {
			if seenDay[m.MealType] == nil {
				seenDay[m.MealType] = map[domain.Weekday]bool{}
			}
			if seenDay[m.MealType][*m.Day] {
				add("%s: another %s is already suggested for %s; pick a different day or null", name, m.MealType, *m.Day)
			}
			seenDay[m.MealType][*m.Day] = true
		}

		if r.Profile.OnePot && !m.OnePot && m.MealType != domain.MealBaking {
			add("%s: the household only cooks one-pot meals; make it one-pot and set one_pot to true", name)
		}
		for _, term := range r.Profile.Avoid {
			if shopping.ContainsTerm(m.Title, term) {
				add("%s: the title mentions %q, which is on the avoid list", name, term)
			}
		}
		for _, ing := range m.Ingredients {
			if !sections[ing.Section] {
				add("%s: ingredient %q uses unknown section %q; use one of the listed section keys", name, ing.Name, ing.Section)
			}
			for _, term := range r.Profile.Avoid {
				if shopping.ContainsTerm(ing.NormalizedName, term) || shopping.ContainsTerm(ing.Name, term) {
					add("%s: ingredient %q is on the avoid list (%q); replace it", name, ing.Name, term)
				}
			}
		}
		if r.Profile.BabyPortions && m.MealType != domain.MealBaking {
			problems = append(problems, babyProblems(m)...)
		}
	}
	return plan, problems
}

// targetProblems checks the number of meals per type against the targets.
func targetProblems(meals []domain.MealJSON, targets map[domain.MealType]int) []string {
	return countProblems(meals, targets, "the week")
}

func countProblems(meals []domain.MealJSON, targets map[domain.MealType]int, what string) []string {
	got := map[domain.MealType]int{}
	for _, m := range meals {
		got[m.MealType]++
	}
	var out []string
	for _, t := range domain.PlannedMealTypes {
		if got[t] != targets[t] {
			out = append(out, fmt.Sprintf("%s must have exactly %d %s meals, not %d", what, targets[t], t, got[t]))
		}
	}
	for t, n := range got {
		if !slices.Contains(domain.PlannedMealTypes, t) {
			out = append(out, fmt.Sprintf("%d meals have meal_type %q; use breakfast, lunch, dinner or baking", n, t))
		}
	}
	return out
}

// mealName names a meal in problem messages: dinner on monday ("Chicken rice").
func mealName(m domain.MealJSON) string {
	if m.Day != nil {
		return fmt.Sprintf("%s on %s (%q)", m.MealType, *m.Day, m.Title)
	}
	return fmt.Sprintf("%s (%q)", m.MealType, m.Title)
}

func babyProblems(m domain.MealJSON) []string {
	first, count := -1, 0
	for i, st := range m.Steps {
		if st.BabyPortion {
			count++
			if first < 0 {
				first = i
			}
		}
	}
	switch {
	case count == 0:
		return []string{fmt.Sprintf("%s: baby portions are on, but no step has baby_portion true; add one before any salt, stock or cheese", mealName(m))}
	case count > 1:
		return []string{fmt.Sprintf("%s: exactly one step may have baby_portion true, found %d", mealName(m), count)}
	}
	var out []string
	for i := 0; i < first; i++ {
		if w := saltyStep.FindString(m.Steps[i].Text); w != "" {
			out = append(out, fmt.Sprintf("%s: step %d adds %q before the baby portion is taken out (step %d); move the baby_portion step before it",
				mealName(m), i+1, w, first+1))
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
