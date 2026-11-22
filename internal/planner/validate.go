package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

// Rules are what a Claude answer is checked against.
type Rules struct {
	Profile     domain.Profile
	SectionKeys []string
	// Day, when set, means exactly one meal for that day (a swap).
	Day domain.Weekday
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

	if r.Day != "" {
		if len(plan.Meals) != 1 {
			add("return exactly one meal (for %s), not %d", r.Day, len(plan.Meals))
		} else if plan.Meals[0].Day != r.Day {
			add("the meal must be for %s, not %s", r.Day, plan.Meals[0].Day)
		}
	}

	sections := map[string]bool{}
	for _, k := range r.SectionKeys {
		sections[k] = true
	}
	seenDay := map[domain.Weekday]bool{}
	for _, m := range plan.Meals {
		if seenDay[m.Day] {
			add("%s has more than one meal", m.Day)
		}
		seenDay[m.Day] = true

		if r.Profile.OnePot && !m.OnePot {
			add("%s (%q): the household only cooks one-pot meals; make it one-pot and set one_pot to true", m.Day, m.Title)
		}
		for _, term := range r.Profile.Avoid {
			if shopping.ContainsTerm(m.Title, term) {
				add("%s (%q): the title mentions %q, which is on the avoid list", m.Day, m.Title, term)
			}
		}
		for _, ing := range m.Ingredients {
			if !sections[ing.Section] {
				add("%s (%q): ingredient %q uses unknown section %q; use one of the listed section keys", m.Day, m.Title, ing.Name, ing.Section)
			}
			for _, term := range r.Profile.Avoid {
				if shopping.ContainsTerm(ing.NormalizedName, term) || shopping.ContainsTerm(ing.Name, term) {
					add("%s (%q): ingredient %q is on the avoid list (%q); replace it", m.Day, m.Title, ing.Name, term)
				}
			}
		}
		if r.Profile.BabyPortions {
			problems = append(problems, babyProblems(m)...)
		}
	}
	return plan, problems
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
		return []string{fmt.Sprintf("%s (%q): baby portions are on, but no step has baby_portion true; add one before any salt, stock or cheese", m.Day, m.Title)}
	case count > 1:
		return []string{fmt.Sprintf("%s (%q): exactly one step may have baby_portion true, found %d", m.Day, m.Title, count)}
	}
	var out []string
	for i := 0; i < first; i++ {
		if w := saltyStep.FindString(m.Steps[i].Text); w != "" {
			out = append(out, fmt.Sprintf("%s (%q): step %d adds %q before the baby portion is taken out (step %d); move the baby_portion step before it",
				m.Day, m.Title, i+1, w, first+1))
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
