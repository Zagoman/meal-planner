package shopping

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// Quantities are summed per dimension. Mass is kept in grams, volume in
// millilitres, spoons in teaspoons; counted things are kept per count noun
// ("pcs", "clove", "can").
const (
	dimMass   = "mass"
	dimVolume = "volume"
	dimSpoon  = "spoon"
	countDim  = "count:" // + singular count noun
	pcs       = "pcs"
)

// canonicalUnit maps a recipe unit to its dimension and the factor that
// converts it to the dimension's base unit.
func canonicalUnit(unit string) (dim string, factor float64) {
	u := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(unit)), ".")
	switch u {
	case "g", "gram", "grams", "gr":
		return dimMass, 1
	case "kg", "kilo", "kilos", "kilogram", "kilograms":
		return dimMass, 1000
	case "ml", "millilitre", "millilitres", "milliliter", "milliliters":
		return dimVolume, 1
	case "cl":
		return dimVolume, 10
	case "dl", "decilitre", "decilitres", "deciliter", "deciliters":
		return dimVolume, 100
	case "l", "litre", "litres", "liter", "liters":
		return dimVolume, 1000
	case "tsp", "teaspoon", "teaspoons", "tsk":
		return dimSpoon, 1
	case "tbsp", "tablespoon", "tablespoons", "spsk":
		return dimSpoon, 3
	case "", "pcs", "pc", "piece", "pieces", "x", "stk", "whole":
		return countDim + pcs, 1
	}
	return countDim + Normalize(u), 1
}

// amounts is a summed quantity per dimension, e.g. {"mass": 750}.
type amounts map[string]float64

func (a amounts) add(qty float64, unit string) {
	dim, f := canonicalUnit(unit)
	a[dim] += qty * f
}

// dims returns the dimensions in a stable order: mass, volume, spoon, then
// count nouns alphabetically.
func (a amounts) dims() []string {
	var out []string
	for _, d := range []string{dimMass, dimVolume, dimSpoon} {
		if _, ok := a[d]; ok {
			out = append(out, d)
		}
	}
	var counts []string
	for d := range a {
		if strings.HasPrefix(d, countDim) {
			counts = append(counts, d)
		}
	}
	slices.Sort(counts)
	return append(out, counts...)
}

// String renders the summed amount, skipping zero quantities
// ("salt, to taste"): "750 g + 2 cloves".
func (a amounts) String() string {
	var parts []string
	for _, d := range a.dims() {
		if a[d] <= 0 {
			continue
		}
		parts = append(parts, formatAmount(d, a[d]))
	}
	return strings.Join(parts, " + ")
}

// formatAmount renders one dimension's base quantity in a readable unit.
func formatAmount(dim string, q float64) string {
	switch dim {
	case dimMass:
		if q >= 1000 {
			return num(q/1000) + " kg"
		}
		return num(math.Round(q)) + " g"
	case dimVolume:
		if q >= 1000 {
			return num(q/1000) + " l"
		}
		return num(math.Round(q)) + " ml"
	case dimSpoon:
		if q >= 3 && isWhole(q/3) {
			return num(q/3) + " tbsp"
		}
		return num(q) + " tsp"
	}
	noun := strings.TrimPrefix(dim, countDim)
	if noun == pcs {
		return num(q)
	}
	if q != 1 {
		noun = plural(noun)
	}
	return num(q) + " " + noun
}

func isWhole(f float64) bool { return math.Abs(f-math.Round(f)) < 1e-9 }

// num formats with at most two decimals and no trailing zeros.
func num(f float64) string {
	return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64)
}

func plural(w string) string {
	switch {
	case w == "":
		return w
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"):
		return w + "es"
	case strings.HasSuffix(w, "y") && len(w) > 1 && !strings.ContainsRune("aeiou", rune(w[len(w)-2])):
		return w[:len(w)-1] + "ies"
	}
	return w + "s"
}
