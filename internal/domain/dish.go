package domain

import (
	"strings"
	"unicode"
)

// DishType picks a recipe's line drawing until the family adds a photo.
type DishType string

const (
	DishSoup     DishType = "soup"
	DishPasta    DishType = "pasta"
	DishFish     DishType = "fish"
	DishCake     DishType = "cake"
	DishOnePot   DishType = "one-pot"
	DishOvenDish DishType = "oven-dish"
)

// Words that pick a drawing, English and Danish, matched against the
// title's words (see hasDishWord).
var dishWords = []struct {
	dish  DishType
	words []string
}{
	{DishSoup, []string{"soup", "suppe", "broth", "chowder", "bisque", "minestrone"}},
	{DishPasta, []string{"pasta", "spaghetti", "lasagne", "lasagna", "penne", "macaroni", "tagliatelle", "fusilli", "linguine", "noodle", "ramen", "gnocchi", "ravioli"}},
	{DishFish, []string{"fish", "fisk", "salmon", "laks", "cod", "torsk", "tuna", "tun", "prawn", "shrimp", "rejer", "mackerel", "makrel", "herring", "sild"}},
	{DishOnePot, []string{"stew", "gryde", "curry", "dal", "dhal", "chilli", "chili", "casserole", "onepot", "one pot", "risotto", "tagine", "goulash"}},
	{DishCake, []string{"cake", "kage", "muffin", "bun", "boller", "bread", "brød", "cookie", "småkage", "pie", "tart"}},
}

// DishTypeOf works out the drawing from the meal: baking is cake, then the
// first title word that matches, then fish by protein, one-pot by flag, and
// an oven dish for everything else. It is never stored.
func DishTypeOf(m Meal) DishType {
	if m.MealType == MealBaking {
		return DishCake
	}
	words := strings.FieldsFunc(strings.ToLower(m.Title), func(r rune) bool { return !unicode.IsLetter(r) })
	for _, d := range dishWords {
		for _, k := range d.words {
			if hasDishWord(words, k) {
				return d.dish
			}
		}
	}
	switch {
	case m.Protein == ProteinFish:
		return DishFish
	case m.OnePot:
		return DishOnePot
	}
	return DishOvenDish
}

// hasDishWord matches a keyword against title words: a whole word, or for
// keywords of four letters or more the start or end of one, so Danish
// compounds count ("fiskefrikadeller", "kyllingesuppe") but "avocado" is not
// "cod". A two-word keyword ("one pot") matches neighbouring words.
func hasDishWord(words []string, k string) bool {
	if a, b, ok := strings.Cut(k, " "); ok {
		for i := 0; i+1 < len(words); i++ {
			if words[i] == a && words[i+1] == b {
				return true
			}
		}
		return false
	}
	k = strings.ReplaceAll(k, "-", "")
	for _, w := range words {
		if w == k || len(k) >= 4 && (strings.HasPrefix(w, k) || strings.HasSuffix(w, k)) {
			return true
		}
	}
	return false
}
