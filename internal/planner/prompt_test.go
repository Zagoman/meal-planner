package planner

import (
	"strings"
	"testing"
	"time"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func profile() domain.Profile {
	return domain.Profile{
		MealTargets: map[domain.MealType]int{domain.MealBreakfast: 7, domain.MealLunch: 2, domain.MealDinner: 4, domain.MealBaking: 1}, People: 3, SpiceLevel: domain.SpiceMild, OnePot: true,
		ProteinMix:   map[domain.Protein]int{domain.ProteinFish: 1, domain.ProteinChicken: 2, domain.ProteinVegetarian: 1},
		Avoid:        []string{"mushrooms", "coriander"},
		Shop:         "Netto",
		BabyPortions: true,
		Language:     "da",
		Cuisines:     []string{"Greek", "Danish classics"},
	}
}

func TestBuildPlanPrompt(t *testing.T) {
	t.Parallel()
	got := BuildPlanPrompt(PromptInput{
		Profile:  profile(),
		Sections: domain.DefaultSections[:2],
		Feedback: []FeedbackLine{
			{Title: "Fish tacos", Protein: "fish", Rating: 5, Note: "kids loved it"},
			{Title: "Lentil soup", Protein: "vegetarian", Rating: 1},
		},
		Request: "Something with pumpkin please",
	})
	for _, want := range []string{
		"People eating: 3",
		"Spice level: mild",
		"One-pot meals only: yes",
		"Baby portions: yes",
		"Cuisines the family loves (lean on them, but keep variety): Greek, Danish classics",
		"chicken 2, fish 1, vegetarian 1",
		`"mushrooms", "coriander"`,
		"Netto",
		`language "da"`,
		"- produce (Fruit & veg)",
		"- bakery (Bakery)",
		`"Fish tacos" (fish): 5/5 - "kids loved it"`,
		`"Lentil soup" (vegetarian): 1/5`,
		"7 breakfasts, 2 lunches, 4 dinners and 1 baking. Exactly that many",
		"<request>\nSomething with pumpkin please\n</request>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "meat_fish") {
		t.Error("prompt lists a section the household does not have")
	}
}

func TestBuildPlanPromptMinimal(t *testing.T) {
	t.Parallel()
	got := BuildPlanPrompt(PromptInput{Profile: domain.Profile{MealTargets: map[domain.MealType]int{domain.MealDinner: 3}, People: 2, Language: "en"}})
	if strings.Contains(got, "Ratings") || strings.Contains(got, "avoid list") || strings.Contains(got, "language") || strings.Contains(got, "Cuisines") {
		t.Errorf("empty sections leaked into prompt:\n%s", got)
	}
	if !strings.Contains(got, "No special request") {
		t.Errorf("missing no-request line:\n%s", got)
	}
}

func TestBuildSystem(t *testing.T) {
	t.Parallel()
	on := BuildSystem(profile())
	if !strings.Contains(on, "Baby portions are ON") || !strings.Contains(on, `"one_pot": true`) {
		t.Errorf("system prompt misses profile rules:\n%s", on)
	}
	if !strings.Contains(on, "Never include a shopping list") {
		t.Error("system prompt must forbid a shopping list")
	}
	off := BuildSystem(domain.Profile{})
	if !strings.Contains(off, "Baby portions are off") || strings.Contains(off, `"one_pot": true on every meal`) {
		t.Errorf("system prompt with rules off:\n%s", off)
	}
}

func TestBuildSwapAndRetryPrompt(t *testing.T) {
	t.Parallel()
	got := BuildSwapPrompt(SwapInput{
		PromptInput: PromptInput{Profile: profile(), Sections: domain.DefaultSections, Request: "faster"},
		MealType:    domain.MealDinner, Day: domain.Wednesday, Current: "Fish pie", OtherMeals: []string{"Chicken curry", "Dal"},
	})
	for _, want := range []string{"exactly one dinner with day wednesday", `replaces "Fish pie"`, `"Chicken curry", "Dal"`, "faster"} {
		if !strings.Contains(got, want) {
			t.Errorf("swap prompt is missing %q\n%s", want, got)
		}
	}
	retry := BuildRetryPrompt([]string{"monday: no baby step", "tuesday: mushrooms"})
	if !strings.Contains(retry, "- monday: no baby step\n- tuesday: mushrooms") || !strings.Contains(retry, "complete corrected JSON") {
		t.Errorf("retry prompt:\n%s", retry)
	}
}

func TestWeekStart(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"2026-11-22": "2026-11-23", // Sunday -> next day
		"2026-11-23": "2026-11-23", // Monday -> same day
		"2026-11-28": "2026-11-30", // Saturday
		"2026-11-24": "2026-11-30", // Tuesday
	}
	for in, want := range cases {
		d, _ := time.Parse(time.DateOnly, in)
		if got := WeekStart(d.Add(15 * time.Hour)).Format(time.DateOnly); got != want {
			t.Errorf("WeekStart(%s) = %s, want %s", in, got, want)
		}
	}
}
