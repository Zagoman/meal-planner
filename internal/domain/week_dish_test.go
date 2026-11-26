package domain_test

import (
	"testing"
	"time"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func TestWeekNumber(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		start string
		want  int
	}{
		{"2026-11-29", 49}, // Sunday start: the week is Sun 29 Nov to Sat 5 Dec
		{"2026-11-23", 48}, // Monday start
		{"2026-12-27", 53}, // across the new year: 2026 has a week 53
	} {
		d, _ := time.Parse("2006-01-02", c.start)
		if got := domain.WeekNumber(d); got != c.want {
			t.Errorf("%s: week %d, want %d", c.start, got, c.want)
		}
	}
}

func TestDishTypeOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		m    domain.Meal
		want domain.DishType
	}{
		{domain.Meal{Title: "Grandma's apple cake", MealType: domain.MealBaking}, domain.DishCake},
		{domain.Meal{Title: "Rye bread", MealType: domain.MealBaking}, domain.DishCake},
		{domain.Meal{Title: "Leek and potato soup", MealType: domain.MealDinner}, domain.DishSoup},
		{domain.Meal{Title: "Spaghetti with meatballs", MealType: domain.MealDinner}, domain.DishPasta},
		{domain.Meal{Title: "Fiskefrikadeller", MealType: domain.MealDinner}, domain.DishFish},
		{domain.Meal{Title: "Baked cod with peas", MealType: domain.MealDinner}, domain.DishFish},
		{domain.Meal{Title: "Lentil dal", MealType: domain.MealDinner}, domain.DishOnePot},
		{domain.Meal{Title: "Sunday roast", MealType: domain.MealDinner, Protein: domain.ProteinFish}, domain.DishFish},
		{domain.Meal{Title: "Chicken and rice", MealType: domain.MealDinner, OnePot: true}, domain.DishOnePot},
		{domain.Meal{Title: "Chicken traybake with peppers", MealType: domain.MealDinner}, domain.DishOvenDish},
		{domain.Meal{Title: "Avocado toast with a piece of cheese", MealType: domain.MealBreakfast}, domain.DishOvenDish},
		{domain.Meal{Title: "Kyllingesuppe", MealType: domain.MealDinner}, domain.DishSoup},
		{domain.Meal{Title: "Chicken & leek one-pot", MealType: domain.MealDinner}, domain.DishOnePot},
		{domain.Meal{Title: "Fish soup with rye bread", MealType: domain.MealDinner}, domain.DishSoup},
	} {
		if got := domain.DishTypeOf(c.m); got != c.want {
			t.Errorf("%q: %s, want %s", c.m.Title, got, c.want)
		}
	}
}
