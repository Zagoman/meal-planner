package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/google/uuid"

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

// qtyLine is "1 bag (1 kg) · need 700 g", or either part alone.
func qtyLine(it domain.ListItem) string {
	switch {
	case it.QtyText != "" && it.NeedText != "":
		return it.QtyText + " · " + it.NeedText
	case it.QtyText != "":
		return it.QtyText
	}
	return it.NeedText
}

// barStyle is the width of the progress bar fill.
func barStyle(done, total int) templ.SafeCSS {
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	return templ.SafeCSS(fmt.Sprintf("width: %d%%;", pct))
}

// shortDay is "Mon".
func shortDay(d domain.Weekday) string {
	if len(d) < 3 {
		return ""
	}
	return strings.ToUpper(string(d[:1])) + string(d[1:3])
}

// mealMeta is "Dinner · 30 min · suggested Wed".
func mealMeta(m domain.Meal) string {
	s := m.MealType.Label() + " · " + itoa(m.Minutes) + " min"
	if m.Day != "" {
		s += " · suggested " + shortDay(m.Day)
	}
	return s
}

// mealCount is "6 meals".
func mealCount(n int) string {
	if n == 1 {
		return "1 meal"
	}
	return itoa(n) + " meals"
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

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// voteText is " · liked by you and Ben · disliked by Anna".
func voteText(m domain.DraftMeal, me uuid.UUID) string {
	var up, down []string
	for _, v := range m.Votes {
		name := v.Name
		if v.MemberID == me {
			name = "you"
		}
		if v.Vote > 0 {
			up = append(up, name)
		} else {
			down = append(down, name)
		}
	}
	out := ""
	if len(up) > 0 {
		out += " · liked by " + joinNames(up)
	}
	if len(down) > 0 {
		out += " · disliked by " + joinNames(down)
	}
	return out
}

func joinNames(n []string) string {
	if len(n) <= 1 {
		return strings.Join(n, "")
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}

// previewCount is every item of a list preview, Check at home included.
func previewCount(l domain.List) int {
	n := len(l.CheckHome)
	for _, s := range l.Sections {
		for _, it := range s.Items {
			if !it.CheckHome {
				n++
			}
		}
	}
	return n
}
