package household

import "strings"

// NormalizeName is the one normalization for pantry items, list items and
// ingredient names, so "Tomatoes " in the pantry matches "tomato" from
// Claude. Rules, in order:
//
//  1. lowercase, trim, collapse inner whitespace to one space;
//  2. singularize the last word only ("cherry tomatoes" -> "cherry tomato"):
//     words of 3 letters or fewer, and words ending in "ss", "us", "is" or
//     listed in invariant, are kept; "ies" -> "y" (berries -> berry);
//     "oes" -> "o" (potatoes -> potato); "ches", "shes", "xes",
//     "sses" drop "es" (peaches -> peach); otherwise a final "s" is dropped
//     (onions -> onion, cheeses -> cheese).
//
// It is deliberately simple ("singular-ish"); idempotent:
// NormalizeName(NormalizeName(x)) == NormalizeName(x).
func NormalizeName(name string) string {
	words := strings.Fields(strings.ToLower(name))
	if len(words) == 0 {
		return ""
	}
	last := len(words) - 1
	words[last] = singular(words[last])
	return strings.Join(words, " ")
}

var invariant = map[string]bool{
	"molasses": true, "hummus": true, "couscous": true, "asparagus": true,
	"swiss": true, "brussels": true, "series": true, "species": true,
}

func singular(w string) string {
	if len(w) <= 3 || invariant[w] {
		return w
	}
	switch {
	case strings.HasSuffix(w, "ss"), strings.HasSuffix(w, "us"), strings.HasSuffix(w, "is"):
		return w
	case strings.HasSuffix(w, "ies") && len(w) > 4: // but pies -> pie
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "oes"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "shes"),
		strings.HasSuffix(w, "xes"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s"):
		return w[:len(w)-1]
	}
	return w
}
