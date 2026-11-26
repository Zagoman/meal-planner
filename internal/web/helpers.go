package web

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// weekNumber is a plan week's number (domain.WeekNumber).
func weekNumber(t time.Time) int { return domain.WeekNumber(t) }

// hasBabyStep reports whether a recipe has a baby-portion step.
func hasBabyStep(steps []domain.Step) bool {
	for _, st := range steps {
		if st.BabyPortion {
			return true
		}
	}
	return false
}

// recipeMeta is a recipe's meta line: "Tuesday · Dinner · 45 min · Serves 4 · One pot".
func recipeMeta(day domain.Weekday, t domain.MealType, minutes, servings int, onePot bool) string {
	parts := []string{}
	if day != "" {
		parts = append(parts, strings.ToUpper(string(day[:1]))+string(day[1:]))
	}
	parts = append(parts, t.Label(), itoa(minutes)+" min", "Serves "+itoa(servings))
	if onePot {
		parts = append(parts, "One pot")
	}
	return strings.Join(parts, " · ")
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

// statsLine is "Rated 4.5 · cooked 3×" or "Not cooked yet".
func statsLine(s domain.RecipeStats) string {
	switch {
	case s.Ratings > 0:
		return fmt.Sprintf("Rated %s · cooked %d×", ratingText(s.Rating), s.TimesCooked)
	case s.TimesCooked > 0:
		return fmt.Sprintf("Cooked %d×", s.TimesCooked)
	}
	return "Not cooked yet"
}

// recipeLine is "Dinner · rated 5 · cooked 3×".
func recipeLine(t domain.MealType, s domain.RecipeStats) string {
	switch {
	case s.Ratings > 0:
		return fmt.Sprintf("%s · rated %s · cooked %d×", t.Label(), ratingText(s.Rating), s.TimesCooked)
	case s.TimesCooked > 0:
		return fmt.Sprintf("%s · cooked %d×", t.Label(), s.TimesCooked)
	}
	return t.Label() + " · not cooked yet"
}

// ratingText is "4" or "4.5".
func ratingText(r float64) string {
	return strconv.FormatFloat(math.Round(r*10)/10, 'f', -1, 64)
}

// inList reports whether an ingredient is added only after the baby
// portion is taken out.
func inList(after []string, ing domain.Ingredient) bool {
	for _, a := range after {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == strings.ToLower(ing.Name) || a == strings.ToLower(ing.NormalizedName) {
			return true
		}
	}
	return false
}

// avoidName is what goes on the avoid list for an ingredient: its
// normalized name ("leek" for "leeks, sliced").
func avoidName(ing domain.Ingredient) string {
	if ing.NormalizedName != "" {
		return ing.NormalizedName
	}
	return strings.ToLower(strings.TrimSpace(ing.Name))
}

// cookURL is step n of cook mode for a recipe or meal page ("/meals/{id}").
func cookURL(recipe string, n int) string {
	return fmt.Sprintf("%s/cook?step=%d", recipe, n)
}

// capitalize upper-cases the first letter: "leek" -> "Leek".
func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// inStrings reports whether s is one of list.
func inStrings(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
