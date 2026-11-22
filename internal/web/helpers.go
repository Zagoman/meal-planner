package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// tabOther shows the navigation without highlighting a tab.
const tabOther Tab = "other"

func tabIf(signedIn bool) Tab {
	if signedIn {
		return tabOther
	}
	return TabNone
}

func statusTitle(status int) string {
	switch status {
	case http.StatusNotFound:
		return "Not found"
	case http.StatusForbidden:
		return "Not allowed"
	case http.StatusUnprocessableEntity:
		return "Check your input"
	}
	if status >= 500 {
		return "Something went wrong"
	}
	return http.StatusText(status)
}

func dayName(d domain.Weekday) string {
	if d == "" {
		return ""
	}
	return strings.ToUpper(string(d[:1])) + string(d[1:])
}

func qty(q float64, unit string) string {
	s := fmt.Sprintf("%g", q)
	if unit == "" || unit == "pcs" {
		return s
	}
	return s + " " + unit
}

func itoa(n int) string { return fmt.Sprint(n) }

// checkVals is the hx-vals for ticking: the desired state, so a replayed
// offline tick is idempotent.
func checkVals(it domain.ListItem) string {
	return fmt.Sprintf(`{"checked":"%t"}`, !it.Checked)
}

func countChecked(l domain.List) (done, total int) {
	for _, s := range l.Sections {
		for _, it := range s.Items {
			total++
			if it.Checked {
				done++
			}
		}
	}
	return done, total
}

// afterBaby reports whether an ingredient is only added after the baby
// portion is taken out.
func afterBaby(m domain.Meal, ing domain.Ingredient) bool {
	for _, a := range m.AfterBabyPortion {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == strings.ToLower(ing.Name) || a == strings.ToLower(ing.NormalizedName) {
			return true
		}
	}
	return false
}

// extraAfterBaby lists after-baby-portion items that are not ingredients
// (usually salt).
func extraAfterBaby(m domain.Meal) []string {
	var out []string
	for _, a := range m.AfterBabyPortion {
		found := false
		for _, ing := range m.Ingredients {
			if strings.EqualFold(a, ing.Name) || strings.EqualFold(a, ing.NormalizedName) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, a)
		}
	}
	return out
}

func proteinLabel(p domain.Protein) string {
	switch p {
	case domain.ProteinChicken:
		return "🍗 chicken"
	case domain.ProteinFish:
		return "🐟 fish"
	case domain.ProteinVegetarian:
		return "🥕 vegetarian"
	}
	return string(p)
}
