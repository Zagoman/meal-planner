package household

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
)

var sectionKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Limits on profile and list sizes, so a single PUT cannot store megabytes.
const (
	maxAvoid    = 50
	maxSections = 50
	maxPantry   = 500
	maxText     = 100
)

func (s *Service) Get(ctx context.Context, sc domain.Scope) (domain.Profile, error) {
	p, err := s.q.GetProfile(ctx, sc.HouseholdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, fmt.Errorf("get profile: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return profileFromDB(p)
}

func (s *Service) Put(ctx context.Context, sc domain.Scope, p domain.Profile) (domain.Profile, error) {
	p, err := cleanProfile(p)
	if err != nil {
		return domain.Profile{}, err
	}
	mix, err := json.Marshal(p.ProteinMix)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("put profile: %w", err)
	}
	row, err := s.q.UpdateProfile(ctx, db.UpdateProfileParams{
		HouseholdID:    sc.HouseholdID,
		DinnersPerWeek: int32(p.DinnersPerWeek), //nolint:gosec // validated 1..7
		Servings:       int32(p.Servings),       //nolint:gosec // validated 1..12
		SpiceLevel:     string(p.SpiceLevel),
		OnePot:         p.OnePot,
		ProteinMix:     mix,
		Avoid:          p.Avoid,
		Shop:           p.Shop,
		ShoppingDay:    string(p.ShoppingDay),
		BabyPortions:   p.BabyPortions,
		Language:       p.Language,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, fmt.Errorf("put profile: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Profile{}, fmt.Errorf("put profile: %w", err)
	}
	return profileFromDB(row)
}

// cleanProfile validates p and normalizes it: avoid entries trimmed,
// de-duplicated and empty ones dropped; language defaults to "en".
func cleanProfile(p domain.Profile) (domain.Profile, error) {
	var problems []string
	if p.DinnersPerWeek < 1 || p.DinnersPerWeek > 7 {
		problems = append(problems, "dinners_per_week: 1 to 7")
	}
	if p.Servings < 1 || p.Servings > 12 {
		problems = append(problems, "servings: 1 to 12")
	}
	switch p.SpiceLevel {
	case domain.SpiceNone, domain.SpiceMild, domain.SpiceMedium, domain.SpiceHot:
	default:
		problems = append(problems, "spice_level: none, mild, medium or hot")
	}
	if !p.ShoppingDay.Valid() {
		problems = append(problems, "shopping_day: a lowercase English weekday")
	}
	mix := map[domain.Protein]int{}
	for k, v := range p.ProteinMix {
		switch k {
		case domain.ProteinChicken, domain.ProteinFish, domain.ProteinVegetarian, domain.ProteinOther:
		default:
			problems = append(problems, fmt.Sprintf("protein_mix: unknown protein %q", k))
			continue
		}
		if v < 0 || v > 7 {
			problems = append(problems, fmt.Sprintf("protein_mix.%s: 0 to 7", k))
			continue
		}
		mix[k] = v
	}
	p.ProteinMix = mix
	seen := map[string]bool{}
	avoid := []string{}
	for _, a := range p.Avoid {
		a = strings.Join(strings.Fields(a), " ")
		key := strings.ToLower(a)
		if a == "" || seen[key] {
			continue
		}
		if len(a) > maxText {
			problems = append(problems, "avoid: entries at most 100 characters")
			break
		}
		seen[key] = true
		avoid = append(avoid, a)
	}
	if len(avoid) > maxAvoid {
		problems = append(problems, "avoid: at most 50 entries")
	}
	p.Avoid = avoid
	p.Shop = strings.TrimSpace(p.Shop)
	if len(p.Shop) > maxText {
		problems = append(problems, "shop: at most 100 characters")
	}
	p.Language = strings.ToLower(strings.TrimSpace(p.Language))
	if p.Language == "" {
		p.Language = "en"
	}
	if len(p.Language) > 20 {
		problems = append(problems, "language: at most 20 characters")
	}
	if len(problems) > 0 {
		return domain.Profile{}, &domain.ValidationError{Problems: problems}
	}
	return p, nil
}

func profileFromDB(p db.Profile) (domain.Profile, error) {
	mix := map[domain.Protein]int{}
	if len(p.ProteinMix) > 0 {
		if err := json.Unmarshal(p.ProteinMix, &mix); err != nil {
			return domain.Profile{}, fmt.Errorf("profile protein_mix: %w", err)
		}
	}
	avoid := p.Avoid
	if avoid == nil {
		avoid = []string{}
	}
	return domain.Profile{
		DinnersPerWeek: int(p.DinnersPerWeek),
		Servings:       int(p.Servings),
		SpiceLevel:     domain.SpiceLevel(p.SpiceLevel),
		OnePot:         p.OnePot,
		ProteinMix:     mix,
		Avoid:          avoid,
		Shop:           p.Shop,
		ShoppingDay:    domain.Weekday(p.ShoppingDay),
		BabyPortions:   p.BabyPortions,
		Language:       p.Language,
	}, nil
}

func (s *Service) Sections(ctx context.Context, sc domain.Scope) ([]domain.Section, error) {
	return sections(ctx, s.q, sc)
}

func sections(ctx context.Context, q *db.Queries, sc domain.Scope) ([]domain.Section, error) {
	rows, err := q.ListSections(ctx, sc.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("sections: %w", err)
	}
	out := make([]domain.Section, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Section{Key: r.Key, Name: r.Name, SortOrder: int(r.SortOrder)})
	}
	return out, nil
}

// PutSections replaces the household's sections. Array order is the shop
// order (sort_order in the input is ignored and rewritten as 10, 20, ...).
// Keys must match ^[a-z][a-z0-9_]{0,39}$, be unique, and not be the
// reserved "pantry_check". At least one section is required.
func (s *Service) PutSections(ctx context.Context, sc domain.Scope, in []domain.Section) ([]domain.Section, error) {
	var problems []string
	if len(in) == 0 {
		problems = append(problems, "sections: at least one section")
	}
	if len(in) > maxSections {
		problems = append(problems, "sections: at most 50")
	}
	seen := map[string]bool{}
	clean := make([]domain.Section, 0, len(in))
	for i, sec := range in {
		key := strings.TrimSpace(sec.Key)
		name := strings.TrimSpace(sec.Name)
		switch {
		case !sectionKeyRe.MatchString(key):
			problems = append(problems, fmt.Sprintf("sections[%d].key: must match ^[a-z][a-z0-9_]{0,39}$", i))
		case key == domain.PantryCheckSection:
			problems = append(problems, fmt.Sprintf("sections[%d].key: %q is reserved", i, key))
		case seen[key]:
			problems = append(problems, fmt.Sprintf("sections[%d].key: duplicate %q", i, key))
		}
		if name == "" || len(name) > maxText {
			problems = append(problems, fmt.Sprintf("sections[%d].name: 1 to 100 characters", i))
		}
		seen[key] = true
		clean = append(clean, domain.Section{Key: key, Name: name, SortOrder: (i + 1) * 10})
	}
	if len(problems) > 0 {
		return nil, &domain.ValidationError{Problems: problems}
	}
	var out []domain.Section
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.DeleteSections(ctx, sc.HouseholdID); err != nil {
			return err
		}
		for _, sec := range clean {
			if err := q.InsertSection(ctx, db.InsertSectionParams{
				HouseholdID: sc.HouseholdID, Key: sec.Key, Name: sec.Name, SortOrder: int32(sec.SortOrder), //nolint:gosec // at most 500
			}); err != nil {
				return err
			}
		}
		var err error
		out, err = sections(ctx, q, sc)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("put sections: %w", err)
	}
	return out, nil
}

func (s *Service) Pantry(ctx context.Context, sc domain.Scope) ([]domain.PantryItem, error) {
	return pantry(ctx, s.q, sc)
}

func pantry(ctx context.Context, q *db.Queries, sc domain.Scope) ([]domain.PantryItem, error) {
	rows, err := q.ListPantryItems(ctx, sc.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("pantry: %w", err)
	}
	out := make([]domain.PantryItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.PantryItem{Name: r.Name, NormalizedName: r.NormalizedName})
	}
	return out, nil
}

// PutPantry replaces the pantry. Names are trimmed; normalized_name is
// NormalizeName(name); empty names and duplicates (by normalized name) are
// dropped, the first spelling wins.
func (s *Service) PutPantry(ctx context.Context, sc domain.Scope, names []string) ([]domain.PantryItem, error) {
	if len(names) > maxPantry {
		return nil, &domain.ValidationError{Problems: []string{"pantry: at most 500 items"}}
	}
	type item struct{ name, norm string }
	var items []item
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.Join(strings.Fields(n), " ")
		norm := NormalizeName(n)
		if norm == "" || seen[norm] {
			continue
		}
		if len(n) > maxText {
			return nil, &domain.ValidationError{Problems: []string{"pantry: names at most 100 characters"}}
		}
		seen[norm] = true
		items = append(items, item{n, norm})
	}
	var out []domain.PantryItem
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.DeletePantryItems(ctx, sc.HouseholdID); err != nil {
			return err
		}
		for _, it := range items {
			if err := q.InsertPantryItem(ctx, db.InsertPantryItemParams{
				HouseholdID: sc.HouseholdID, Name: it.name, NormalizedName: it.norm,
			}); err != nil {
				return err
			}
		}
		var err error
		out, err = pantry(ctx, q, sc)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("put pantry: %w", err)
	}
	return out, nil
}
