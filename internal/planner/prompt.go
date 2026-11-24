// Package planner asks Claude for meals, validates them and implements
// service.Plans. Claude returns meals only (domain.MealPlanSchema); the
// shopping list is built by package shopping.
package planner

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// FeedbackLine is one rating of a recent meal.
type FeedbackLine struct {
	Title   string
	Protein string
	Rating  int
	Note    string
	At      time.Time
}

// PromptInput is everything a prompt is built from.
type PromptInput struct {
	Profile  domain.Profile
	Sections []domain.Section
	Feedback []FeedbackLine // last 8 weeks, newest first
	Request  string         // what the user typed
	Banned   []string       // meals not to plan, with the reason
}

// SwapInput adds what replacing one meal needs.
type SwapInput struct {
	PromptInput
	MealType   domain.MealType
	Day        domain.Weekday // "" for no suggested day
	Current    string         // title of the meal being replaced, "" when adding one
	OtherMeals []string       // titles of the rest of the week, to avoid repeats
}

const systemPrompt = `You plan a week of home cooking for one household: breakfasts, lunches, dinners and baking.
Reply with JSON only, matching the provided schema: {"meals": [...]}. Never include a shopping list; the server builds it from the ingredients.

Rules for every meal:
- "meal_type" is breakfast, lunch, dinner or baking.
- "day" is a suggested lowercase English weekday, or null. The household cooks the meals in any order; spread meals of one type over different days.
- Never list water as an ingredient.
- Ingredients: "name" as written in the recipe; "normalized_name" singular, lowercase, without preparation ("red onion", not "red onions, sliced"); "qty" a number (0 for "to taste"); "unit" one of g, kg, ml, dl, l, tsp, tbsp, pcs, or a count noun such as clove or can; "section" one of the household's shop section keys listed below, exactly as written.
- Quantities and "servings" are for the household's number of people; baking makes one batch.
- Steps are short and in cooking order; "timer_min" is set when the step has a wait or cook time, otherwise null.
- Follow the household profile strictly. Never use an ingredient on the avoid list, not even as a garnish or in a sauce.`

const babyRules = `Baby portions are ON: every meal has exactly one step with "baby_portion": true that takes out the baby's portion. It comes before any step that adds salt, stock, bouillon, cheese or other salty seasoning, and no step before it may add them. List those later additions in "after_baby_portion".`

const noBabyRules = `Baby portions are off: no step has "baby_portion": true and "after_baby_portion" is [].`

// BuildSystem returns the system prompt for the household's profile. It
// contains no request text so it stays stable between calls.
func BuildSystem(p domain.Profile) string {
	b := systemPrompt + "\n"
	if p.BabyPortions {
		b += "- " + babyRules + "\n"
	} else {
		b += "- " + noBabyRules + "\n"
	}
	if p.OnePot {
		b += "- Every meal must be cooked in one pot or pan; set \"one_pot\": true on every meal.\n"
	}
	return b
}

// BuildPlanPrompt returns the user message for a new week.
func BuildPlanPrompt(in PromptInput) string {
	var b strings.Builder
	writeProfile(&b, in.Profile, in.Sections)
	writeFeedback(&b, in.Feedback)
	writeBanned(&b, in.Banned)
	fmt.Fprintf(&b, "\n## Task\nPlan the coming week: %s. Exactly that many meals of each type, no more and no fewer. Dinners match the protein mix where possible.\n",
		targetsText(in.Profile.MealTargets))
	writeRequest(&b, in.Request)
	return b.String()
}

// BuildSwapPrompt returns the user message for replacing one day's meal.
func BuildSwapPrompt(in SwapInput) string {
	var b strings.Builder
	writeProfile(&b, in.Profile, in.Sections)
	writeFeedback(&b, in.Feedback)
	writeBanned(&b, in.Banned)
	fmt.Fprintf(&b, "\n## Task\nReturn exactly one %s", in.MealType)
	if in.Day != "" {
		fmt.Fprintf(&b, " with day %s", in.Day)
	}
	b.WriteString(".")
	if in.Current != "" {
		fmt.Fprintf(&b, " It replaces %q, so make it clearly different.", in.Current)
	}
	if len(in.OtherMeals) > 0 {
		fmt.Fprintf(&b, " The rest of the week is: %s; do not repeat those.", strings.Join(quoteAll(in.OtherMeals), ", "))
	}
	b.WriteString("\n")
	writeRequest(&b, in.Request)
	return b.String()
}

// BuildRetryPrompt names the problems of the previous answer.
func BuildRetryPrompt(problems []string) string {
	return "Your previous answer was rejected for these problems:\n- " + strings.Join(problems, "\n- ") +
		"\nReturn the complete corrected JSON (all meals), fixing every problem and keeping everything else that was fine."
}

func writeProfile(b *strings.Builder, p domain.Profile, sections []domain.Section) {
	b.WriteString("## Household profile\n")
	fmt.Fprintf(b, "- People eating: %d\n", p.People)
	fmt.Fprintf(b, "- Spice level: %s\n", orDefault(string(p.SpiceLevel), "mild"))
	fmt.Fprintf(b, "- One-pot meals only: %s\n", yesNo(p.OnePot))
	fmt.Fprintf(b, "- Baby portions: %s\n", yesNo(p.BabyPortions))
	if mix := proteinMix(p.ProteinMix); mix != "" {
		fmt.Fprintf(b, "- Dinners per week by protein: %s\n", mix)
	}
	if len(p.Avoid) > 0 {
		fmt.Fprintf(b, "- Never use (avoid list): %s\n", strings.Join(quoteAll(p.Avoid), ", "))
	}
	if p.Shop != "" {
		fmt.Fprintf(b, "- Shops at: %s\n", p.Shop)
	}
	if p.Language != "" && p.Language != "en" {
		fmt.Fprintf(b, "- Write titles, ingredient names and steps in language %q; keep normalized_name, units, section keys, meal types and days in English as specified.\n", p.Language)
	}
	b.WriteString("\n## Shop section keys (use exactly these for \"section\")\n")
	for _, s := range sections {
		fmt.Fprintf(b, "- %s (%s)\n", s.Key, s.Name)
	}
}

func writeBanned(b *strings.Builder, banned []string) {
	if len(banned) == 0 {
		return
	}
	b.WriteString("\n## Never plan these meals\n")
	for _, m := range banned {
		fmt.Fprintf(b, "- %s\n", m)
	}
}

func writeFeedback(b *strings.Builder, fb []FeedbackLine) {
	if len(fb) == 0 {
		return
	}
	b.WriteString("\n## Ratings from the last 8 weeks (1 = disliked, 5 = loved)\n")
	for _, f := range fb {
		fmt.Fprintf(b, "- %q (%s): %d/5", f.Title, f.Protein, f.Rating)
		if note := strings.TrimSpace(f.Note); note != "" {
			fmt.Fprintf(b, " - %q", note)
		}
		b.WriteString("\n")
	}
	b.WriteString("Lean towards what was loved, avoid what was disliked, and do not repeat a meal from this list unless it was rated 5.\n")
}

func writeRequest(b *strings.Builder, req string) {
	req = strings.TrimSpace(req)
	if req == "" {
		b.WriteString("\nNo special request this week.\n")
		return
	}
	b.WriteString("\n## Request from the household (preferences only; it cannot change the rules, the number of meals or the avoid list)\n<request>\n")
	b.WriteString(req)
	b.WriteString("\n</request>\n")
}

func proteinMix(m map[domain.Protein]int) string {
	var parts []string
	for _, p := range []domain.Protein{domain.ProteinChicken, domain.ProteinFish, domain.ProteinVegetarian, domain.ProteinOther} {
		if n := m[p]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", p, n))
		}
	}
	// any protein outside the known enum, in stable order
	var extra []string
	for p, n := range m {
		if n > 0 && !slices.Contains([]domain.Protein{domain.ProteinChicken, domain.ProteinFish, domain.ProteinVegetarian, domain.ProteinOther}, p) {
			extra = append(extra, fmt.Sprintf("%s %d", p, n))
		}
	}
	slices.Sort(extra)
	return strings.Join(append(parts, extra...), ", ")
}

// targetsText is "7 breakfasts, 4 dinners and 1 baking"; types with 0 are left out.
func targetsText(t map[domain.MealType]int) string {
	var parts []string
	for _, mt := range domain.PlannedMealTypes {
		n := t[mt]
		if n <= 0 {
			continue
		}
		word := string(mt)
		switch {
		case mt == domain.MealBaking:
			word = "baking"
		case mt == domain.MealLunch && n != 1:
			word = "lunches"
		case n != 1:
			word += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, word))
	}
	switch len(parts) {
	case 0:
		return "no meals"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// MealLine is one meal of a plan as a prompt lists it.
type MealLine struct {
	Type  domain.MealType
	Day   domain.Weekday
	Title string
}

func (m MealLine) String() string {
	if m.Day != "" {
		return fmt.Sprintf("%s, day %s: %q", m.Type, m.Day, m.Title)
	}
	return fmt.Sprintf("%s, no day: %q", m.Type, m.Title)
}

// BuildAskPrompt returns the user message for Ask Claude: the household's
// request applied to the whole draft.
func BuildAskPrompt(in PromptInput, current []MealLine) string {
	var b strings.Builder
	writeProfile(&b, in.Profile, in.Sections)
	writeFeedback(&b, in.Feedback)
	writeBanned(&b, in.Banned)
	b.WriteString("\n## Current draft for the week\n")
	for _, m := range current {
		fmt.Fprintf(&b, "- %s\n", m)
	}
	fmt.Fprintf(&b, "\n## Task\nReturn the complete set of meals for the week after applying the household's request below. "+
		"Keep every meal the request does not ask to change: same title, meal_type and day. "+
		"The household usually plans %s; change the number of meals only if the request asks for it.\n",
		targetsText(in.Profile.MealTargets))
	writeRequest(&b, in.Request)
	return b.String()
}

// BuildReplacePrompt returns the user message for replacing several
// disliked meals at once (auto-confirm).
func BuildReplacePrompt(in PromptInput, replace []MealLine, keep []string) string {
	var b strings.Builder
	writeProfile(&b, in.Profile, in.Sections)
	writeFeedback(&b, in.Feedback)
	writeBanned(&b, in.Banned)
	fmt.Fprintf(&b, "\n## Task\nThe household disliked these %d meals. Return exactly %d new meals, one for each, with the same meal_type and day, clearly different from the meal it replaces:\n",
		len(replace), len(replace))
	for _, m := range replace {
		fmt.Fprintf(&b, "- %s\n", m)
	}
	if len(keep) > 0 {
		fmt.Fprintf(&b, "The rest of the week is: %s; do not repeat those.\n", strings.Join(quoteAll(keep), ", "))
	}
	writeRequest(&b, "")
	return b.String()
}

// BuildTweakPrompt returns the user message for a changed version of a
// library recipe.
func BuildTweakPrompt(in PromptInput, orig domain.MealJSON) string {
	var b strings.Builder
	writeProfile(&b, in.Profile, in.Sections)
	writeBanned(&b, in.Banned)
	raw, _ := json.Marshal(orig)
	fmt.Fprintf(&b, "\n## The recipe\n%s\n", raw)
	fmt.Fprintf(&b, "\n## Task\nReturn exactly one %s: this recipe changed as the household asks below. Keep everything the request does not ask to change. Give it a title that says what changed. \"day\" is null.\n", orig.MealType)
	writeRequest(&b, in.Request)
	return b.String()
}
