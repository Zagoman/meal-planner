// Package shopping builds the shopping list from a plan's meals and
// implements service.Lists.
//
// The builder (Build, Group) is pure and holds the list rules: merge by
// normalized name, sum convertible units, round up to whole buying units and
// show the amount needed only when it differs, never list water, order by
// the household's sections, and flag pantry items for "Check at home".
package shopping

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// Item is one generated shopping-list line before it is stored.
type Item struct {
	Name           string
	NormalizedName string
	QtyText        string // what to buy: "1 net", or the summed amount "750 g"
	NeedText       string // what the recipes need when it differs from QtyText: "need 3"
	Section        string // the real shop section, also for pantry items
	CheckHome      bool   // on the household's pantry: shown under Check at home
}

// buyingUnit is how a shop sells something: a net of 5 onions, a 1 kg bag.
type buyingUnit struct {
	dim    string  // the summed dimension it applies to
	size   float64 // base units per pack (g, ml, tsp or count)
	label  string  // singular, "net"
	plural string  // "nets"
	name   string  // display name of the item, optional
}

// buyingUnits is a small built-in table; anything else falls back to the
// summed amount.
var buyingUnits = map[string][]buyingUnit{
	"onion":         {{dim: countDim + pcs, size: 5, label: "net", plural: "nets", name: "Onions"}},
	"red onion":     {{dim: countDim + pcs, size: 3, label: "net", plural: "nets", name: "Red onions"}},
	"garlic":        {{dim: countDim + "clove", size: 10, label: "head", plural: "heads", name: "Garlic"}},
	"garlic clove":  {{dim: countDim + pcs, size: 10, label: "head", plural: "heads", name: "Garlic"}},
	"potato":        {{dim: dimMass, size: 1000, label: "bag (1 kg)", plural: "bags (1 kg)", name: "Potatoes"}},
	"carrot":        {{dim: dimMass, size: 1000, label: "bag (1 kg)", plural: "bags (1 kg)", name: "Carrots"}, {dim: countDim + pcs, size: 8, label: "bag", plural: "bags", name: "Carrots"}},
	"rice":          {{dim: dimMass, size: 1000, label: "bag (1 kg)", plural: "bags (1 kg)"}},
	"pasta":         {{dim: dimMass, size: 500, label: "pack (500 g)", plural: "packs (500 g)"}},
	"spaghetti":     {{dim: dimMass, size: 500, label: "pack (500 g)", plural: "packs (500 g)"}},
	"butter":        {{dim: dimMass, size: 250, label: "pack (250 g)", plural: "packs (250 g)"}},
	"milk":          {{dim: dimVolume, size: 1000, label: "carton (1 l)", plural: "cartons (1 l)"}},
	"cream":         {{dim: dimVolume, size: 250, label: "carton (250 ml)", plural: "cartons (250 ml)"}},
	"egg":           {{dim: countDim + pcs, size: 6, label: "box (6)", plural: "boxes (6)", name: "Eggs"}},
	"chicken thigh": {{dim: dimMass, size: 500, label: "pack (500 g)", plural: "packs (500 g)", name: "Chicken thighs"}},
	"chicken breast": {
		{dim: dimMass, size: 500, label: "pack (500 g)", plural: "packs (500 g)", name: "Chicken breasts"},
	},
	"minced beef": {{dim: dimMass, size: 500, label: "pack (500 g)", plural: "packs (500 g)"}},
}

// Build merges ingredients into shopping-list items, ordered like the
// household's sections. Ingredients whose normalized name is on the pantry
// list are flagged CheckHome and keep their section. A section key the
// household does not have falls back to its last section (normally "other").
// Water is left out. The result never contains two items with the same
// NormalizedName.
func Build(ingredients []domain.Ingredient, sections []domain.Section, pantry []string) []Item {
	known := map[string]bool{}
	for _, s := range sections {
		known[s.Key] = true
	}
	fallback := "other"
	if len(sections) > 0 {
		fallback = sortedSections(sections)[len(sections)-1].Key
	}
	onPantry := map[string]bool{}
	for _, p := range pantry {
		onPantry[Normalize(p)] = true
	}

	type acc struct {
		key     string
		section string
		amounts amounts
	}
	byKey := map[string]*acc{}
	var order []string
	for _, ing := range ingredients {
		key := Normalize(ing.NormalizedName)
		if key == "" {
			key = Normalize(ing.Name)
		}
		if key == "" || IsWater(key) || IsWater(ing.Name) {
			continue
		}
		a := byKey[key]
		if a == nil {
			sec := ing.Section
			if !known[sec] {
				sec = fallback
			}
			a = &acc{key: key, section: sec, amounts: amounts{}}
			byKey[key] = a
			order = append(order, key)
		}
		a.amounts.add(ing.Qty, ing.Unit)
	}

	items := make([]Item, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		it := Item{NormalizedName: key, Section: a.section, Name: displayName(key), CheckHome: onPantry[key]}
		it.QtyText, it.NeedText = quantityTexts(key, a.amounts)
		if bu, ok := matchBuyingUnit(key, a.amounts); ok && bu.name != "" {
			it.Name = bu.name
		}
		items = append(items, it)
	}

	rank := sectionRank(sections)
	slices.SortStableFunc(items, func(x, y Item) int {
		if rx, ry := rank(x.Section), rank(y.Section); rx != ry {
			return rx - ry
		}
		return strings.Compare(x.NormalizedName, y.NormalizedName)
	})
	return items
}

// quantityTexts returns ("1 net", "need 3") when a buying unit fits, else
// (summed amount, ""). Buying units and counted things are always whole
// ("1 net", never "0.5 net"; 1.5 lemons is "2"); "need" is shown only when
// it differs from what is bought.
func quantityTexts(key string, a amounts) (qty, need string) {
	if bu, ok := matchBuyingUnit(key, a); ok {
		total := a[bu.dim]
		if total <= 0 {
			return "", ""
		}
		packs := math.Ceil(total/bu.size - 1e-9)
		label := bu.label
		if packs != 1 {
			label = bu.plural
		}
		qty = num(packs) + " " + label
		if math.Abs(packs*bu.size-total) > 1e-9 {
			need = "need " + formatAmount(bu.dim, total)
		}
		return qty, need
	}
	whole := amounts{}
	for d, q := range a {
		if strings.HasPrefix(d, countDim) {
			q = math.Ceil(q - 1e-9)
		}
		whole[d] = q
	}
	return whole.String(), ""
}

// matchBuyingUnit applies only when everything needed is in one dimension
// the table knows; mixed units fall back to the summed amount.
func matchBuyingUnit(key string, a amounts) (buyingUnit, bool) {
	dims := a.dims()
	if len(dims) != 1 {
		return buyingUnit{}, false
	}
	for _, bu := range buyingUnits[key] {
		if bu.dim == dims[0] {
			return bu, true
		}
	}
	return buyingUnit{}, false
}

func displayName(key string) string {
	r, n := utf8.DecodeRuneInString(key)
	return string(unicode.ToUpper(r)) + key[n:]
}

func sortedSections(sections []domain.Section) []domain.Section {
	out := slices.Clone(sections)
	slices.SortStableFunc(out, func(a, b domain.Section) int {
		if a.SortOrder != b.SortOrder {
			return a.SortOrder - b.SortOrder
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// sectionRank orders keys by the household's order; unknown keys come after
// known ones and domain.PantryCheckSection is always last.
func sectionRank(sections []domain.Section) func(string) int {
	idx := map[string]int{}
	for i, s := range sortedSections(sections) {
		idx[s.Key] = i
	}
	return func(key string) int {
		if key == domain.PantryCheckSection {
			return len(idx) + 1
		}
		if i, ok := idx[key]; ok {
			return i
		}
		return len(idx)
	}
}

// PantryCheckName is the display name of the Check at home group.
const PantryCheckName = "Check at home"

// Group arranges stored items into the display list. CheckHome holds every
// pantry item (answered or not), sorted by name. Sections are the
// household's sections in order (empty ones omitted), unknown section keys
// in a group after them, holding everything to buy: pantry items appear
// there only once answered Buy. Inside a section items are sorted by name,
// so ticking never moves an item.
func Group(planID uuid.UUID, items []domain.ListItem, sections []domain.Section) domain.List {
	names := map[string]string{}
	for _, s := range sections {
		names[s.Key] = s.Name
	}
	rank := sectionRank(sections)
	byName := func(a, b domain.ListItem) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	}
	list := domain.List{PlanID: planID, CheckHome: []domain.ListItem{}, Sections: []domain.ListSection{}}
	byKey := map[string]*domain.ListSection{}
	var keys []string
	for _, it := range items {
		if it.CheckHome {
			list.CheckHome = append(list.CheckHome, it)
		}
		if !it.ToBuy() {
			continue
		}
		key := it.Section
		if key == domain.PantryCheckSection { // legacy rows
			key = "other"
		}
		ls := byKey[key]
		if ls == nil {
			name, ok := names[key]
			if !ok {
				name = displayName(strings.ReplaceAll(key, "_", " "))
			}
			ls = &domain.ListSection{Key: key, Name: name}
			byKey[key] = ls
			keys = append(keys, key)
		}
		ls.Items = append(ls.Items, it)
	}
	slices.SortStableFunc(list.CheckHome, byName)
	slices.SortStableFunc(keys, func(a, b string) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	for _, k := range keys {
		ls := byKey[k]
		// Shopping list fixes #1: items in the basket sink to the end of
		// their section, so what is left to buy stays on top.
		slices.SortStableFunc(ls.Items, func(a, b domain.ListItem) int {
			if a.Checked != b.Checked {
				if a.Checked {
					return 1
				}
				return -1
			}
			return byName(a, b)
		})
		list.Sections = append(list.Sections, *ls)
	}
	return list
}
