package shopping

import (
	"strings"
	"unicode"
)

// Normalize turns an ingredient or pantry name into the key used to merge
// shopping-list items and to compare against the avoid and pantry lists.
//
// Contract (stable; package A uses it for pantry names):
//   - lowercase;
//   - everything from the first ',' or '(' on is dropped
//     ("onions, chopped" -> "onion", "tomatoes (tinned)" -> "tomato");
//   - every rune that is not a letter or digit (any script) becomes a space,
//     so "long-grain rice" -> "long grain rice";
//   - whitespace is collapsed and trimmed;
//   - the last word is made singular with simple English rules
//     (berries -> berry, tomatoes -> tomato, radishes -> radish,
//     thighs -> thigh); words ending in ss/us/is are left alone;
//   - the result may be "" for input with no letters or digits.
//
// Normalize is idempotent: Normalize(Normalize(s)) == Normalize(s).
func Normalize(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexAny(s, ",("); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	words[len(words)-1] = singular(words[len(words)-1])
	return strings.Join(words, " ")
}

// invariant words end in "s" but are already singular.
var invariant = map[string]bool{
	"molasses": true, "hummus": true, "couscous": true, "asparagus": true,
	"swiss": true, "brussels": true, "series": true, "species": true,
}

func singular(w string) string {
	if len([]rune(w)) <= 3 || invariant[w] {
		return w
	}
	switch {
	case strings.HasSuffix(w, "ies") && len([]rune(w)) > 4: // berries -> berry, but pies -> pie
		return strings.TrimSuffix(w, "ies") + "y"
	case strings.HasSuffix(w, "oes"):
		return strings.TrimSuffix(w, "es")
	case strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "shes"),
		strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "xes"), strings.HasSuffix(w, "zes"):
		return strings.TrimSuffix(w, "es")
	case strings.HasSuffix(w, "ss"), strings.HasSuffix(w, "us"), strings.HasSuffix(w, "is"):
		return w
	case strings.HasSuffix(w, "s"):
		return strings.TrimSuffix(w, "s")
	}
	return w
}

// ContainsTerm reports whether the normalized name contains the normalized
// term as a whole-word sequence: "chestnut mushroom" contains "mushroom",
// "peanut" does not contain "nut".
func ContainsTerm(name, term string) bool {
	n, t := Normalize(name), Normalize(term)
	if t == "" || n == "" {
		return false
	}
	return n == t || strings.HasPrefix(n, t+" ") || strings.HasSuffix(n, " "+t) || strings.Contains(n, " "+t+" ")
}
