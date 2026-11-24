package household

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
	return getProfile(ctx, s.q, sc)
}

func getProfile(ctx context.Context, q *db.Queries, sc domain.Scope) (domain.Profile, error) {
	p, err := q.GetProfile(ctx, sc.HouseholdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Profile{}, fmt.Errorf("get profile: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Profile{}, fmt.Errorf("get profile: %w", err)
	}
	out, err := profileFromDB(p)
	if err != nil {
		return domain.Profile{}, err
	}
	items, err := avoidItems(ctx, q, sc)
	if err != nil {
		return domain.Profile{}, err
	}
	for _, a := range items {
		out.Avoid = append(out.Avoid, a.Name)
	}
	return out, nil
}

// Put saves the profile. p.Avoid replaces the avoid list when it is not nil
// (names already on the list keep their entry); nil leaves the list alone.
func (s *Service) Put(ctx context.Context, sc domain.Scope, p domain.Profile) (domain.Profile, error) {
	replaceAvoid := p.Avoid != nil
	p, err := cleanProfile(p)
	if err != nil {
		return domain.Profile{}, err
	}
	mix, err := json.Marshal(p.ProteinMix)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("put profile: %w", err)
	}
	targets, err := json.Marshal(p.MealTargets)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("put profile: %w", err)
	}
	clock, _ := parseClock(p.AutoConfirmTime) // validated by cleanProfile
	var out domain.Profile
	err = s.inTx(ctx, func(q *db.Queries) error {
		_, err := q.UpdateProfile(ctx, db.UpdateProfileParams{
			HouseholdID:     sc.HouseholdID,
			MealTargets:     targets,
			People:          int32(p.People), //nolint:gosec // validated 1..12
			SpiceLevel:      string(p.SpiceLevel),
			OnePot:          p.OnePot,
			ProteinMix:      mix,
			Shop:            p.Shop,
			ShoppingDay:     string(p.ShoppingDay),
			BabyPortions:    p.BabyPortions,
			Language:        p.Language,
			AutoConfirmTime: clock,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if replaceAvoid {
			keep := make([]string, 0, len(p.Avoid))
			for _, name := range p.Avoid {
				keep = append(keep, NormalizeName(name))
				if _, err := q.InsertAvoidItem(ctx, db.InsertAvoidItemParams{
					HouseholdID: sc.HouseholdID, Name: name, NormalizedName: NormalizeName(name),
					AddedBy: memberRef(sc), Source: string(domain.AvoidFromProfile),
				}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			if err := q.DeleteAvoidItemsNotIn(ctx, db.DeleteAvoidItemsNotInParams{HouseholdID: sc.HouseholdID, Keep: keep}); err != nil {
				return err
			}
		}
		out, err = getProfile(ctx, q, sc)
		return err
	})
	if err != nil {
		return domain.Profile{}, fmt.Errorf("put profile: %w", err)
	}
	return out, nil
}

// cleanProfile validates p and normalizes it: avoid entries trimmed,
// de-duplicated (by normalized name) and empty ones dropped; language
// defaults to "en"; auto-confirm time to 08:00.
func cleanProfile(p domain.Profile) (domain.Profile, error) {
	var problems []string
	targets := map[domain.MealType]int{}
	for k, v := range p.MealTargets {
		if !slices.Contains(domain.PlannedMealTypes, k) {
			problems = append(problems, fmt.Sprintf("meal_targets: unknown meal type %q", k))
			continue
		}
		if v < 0 || v > domain.MaxMealsPerType {
			problems = append(problems, fmt.Sprintf("meal_targets.%s: 0 to %d", k, domain.MaxMealsPerType))
			continue
		}
		targets[k] = v
	}
	for _, t := range domain.PlannedMealTypes {
		if _, ok := targets[t]; !ok {
			targets[t] = 0
		}
	}
	p.MealTargets = targets
	if len(problems) == 0 && p.TotalMeals() == 0 {
		problems = append(problems, "meal_targets: at least one meal a week")
	}
	if p.People < 1 || p.People > 12 {
		problems = append(problems, "people: 1 to 12")
	}
	switch p.SpiceLevel {
	case domain.SpiceNone, domain.SpiceMild, domain.SpiceMedium, domain.SpiceHot:
	default:
		problems = append(problems, "spice_level: none, mild, medium or hot")
	}
	if !p.ShoppingDay.Valid() {
		problems = append(problems, "shopping_day: a lowercase English weekday")
	}
	if strings.TrimSpace(p.AutoConfirmTime) == "" {
		p.AutoConfirmTime = "08:00"
	}
	if _, err := parseClock(p.AutoConfirmTime); err != nil {
		problems = append(problems, "auto_confirm_time: HH:MM, 24-hour")
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
	avoid, avoidProblems := cleanAvoid(p.Avoid)
	problems = append(problems, avoidProblems...)
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

func cleanAvoid(in []string) ([]string, []string) {
	var problems []string
	seen := map[string]bool{}
	avoid := []string{}
	for _, a := range in {
		a = strings.Join(strings.Fields(a), " ")
		key := NormalizeName(a)
		if key == "" || seen[key] {
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
	return avoid, problems
}

var clockRe = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):([0-5][0-9])$`)

// parseClock reads "08:00" as a time of day.
func parseClock(s string) (pgtype.Time, error) {
	m := clockRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return pgtype.Time{}, fmt.Errorf("bad time %q", s)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	return pgtype.Time{Microseconds: int64(h*60+mi) * 60_000_000, Valid: true}, nil
}

func profileFromDB(p db.Profile) (domain.Profile, error) {
	mix := map[domain.Protein]int{}
	if len(p.ProteinMix) > 0 {
		if err := json.Unmarshal(p.ProteinMix, &mix); err != nil {
			return domain.Profile{}, fmt.Errorf("profile protein_mix: %w", err)
		}
	}
	targets := map[domain.MealType]int{}
	if len(p.MealTargets) > 0 {
		if err := json.Unmarshal(p.MealTargets, &targets); err != nil {
			return domain.Profile{}, fmt.Errorf("profile meal_targets: %w", err)
		}
	}
	for _, t := range domain.PlannedMealTypes {
		if _, ok := targets[t]; !ok {
			targets[t] = 0
		}
	}
	clock := "08:00"
	if p.AutoConfirmTime.Valid {
		m := p.AutoConfirmTime.Microseconds / 60_000_000
		clock = fmt.Sprintf("%02d:%02d", m/60, m%60)
	}
	return domain.Profile{
		MealTargets:     targets,
		People:          int(p.People),
		SpiceLevel:      domain.SpiceLevel(p.SpiceLevel),
		OnePot:          p.OnePot,
		ProteinMix:      mix,
		Avoid:           []string{},
		Shop:            p.Shop,
		ShoppingDay:     domain.Weekday(p.ShoppingDay),
		AutoConfirmTime: clock,
		BabyPortions:    p.BabyPortions,
		Language:        p.Language,
	}, nil
}

// AvoidItems is the household's avoid list, by name.
func (s *Service) AvoidItems(ctx context.Context, sc domain.Scope) ([]domain.AvoidItem, error) {
	return avoidItems(ctx, s.q, sc)
}

func avoidItems(ctx context.Context, q *db.Queries, sc domain.Scope) ([]domain.AvoidItem, error) {
	rows, err := q.ListAvoidItems(ctx, sc.HouseholdID)
	if err != nil {
		return nil, fmt.Errorf("avoid items: %w", err)
	}
	out := make([]domain.AvoidItem, len(rows))
	for i, r := range rows {
		out[i] = avoidFromDB(r)
	}
	return out, nil
}

// AddAvoid adds one name to the avoid list; a name already on it (by
// normalized name) returns the existing entry.
func (s *Service) AddAvoid(ctx context.Context, sc domain.Scope, name string, source domain.AvoidSource) (domain.AvoidItem, error) {
	name = strings.Join(strings.Fields(name), " ")
	norm := NormalizeName(name)
	var problems []string
	if norm == "" {
		problems = append(problems, "name is required")
	}
	if len(name) > maxText {
		problems = append(problems, "name: at most 100 characters")
	}
	switch source {
	case domain.AvoidFromSetup, domain.AvoidFromRecipe, domain.AvoidFromProfile:
	default:
		problems = append(problems, fmt.Sprintf("unknown source %q", source))
	}
	if len(problems) > 0 {
		return domain.AvoidItem{}, &domain.ValidationError{Problems: problems}
	}
	var out domain.AvoidItem
	err := s.inTx(ctx, func(q *db.Queries) error {
		row, err := q.InsertAvoidItem(ctx, db.InsertAvoidItemParams{
			HouseholdID: sc.HouseholdID, Name: name, NormalizedName: norm,
			AddedBy: memberRef(sc), Source: string(source),
		})
		if err == nil {
			out = avoidFromDB(row)
			rows, err := q.ListAvoidItems(ctx, sc.HouseholdID)
			if err != nil {
				return err
			}
			if len(rows) > maxAvoid {
				return &domain.ValidationError{Problems: []string{"avoid: at most 50 entries"}}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := q.ListAvoidItems(ctx, sc.HouseholdID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.NormalizedName == norm {
				out = avoidFromDB(r)
				return nil
			}
		}
		return errors.New("avoid item vanished")
	})
	if err != nil {
		return domain.AvoidItem{}, fmt.Errorf("add avoid item: %w", err)
	}
	return out, nil
}

// DeleteAvoid removes one entry of the avoid list.
func (s *Service) DeleteAvoid(ctx context.Context, sc domain.Scope, id uuid.UUID) error {
	n, err := s.q.DeleteAvoidItem(ctx, db.DeleteAvoidItemParams{ID: id, HouseholdID: sc.HouseholdID})
	if err != nil {
		return fmt.Errorf("delete avoid item: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete avoid item: %w", domain.ErrNotFound)
	}
	return nil
}

func avoidFromDB(r db.AvoidItem) domain.AvoidItem {
	return domain.AvoidItem{
		ID: r.ID, Name: r.Name, NormalizedName: r.NormalizedName,
		Source: domain.AvoidSource(r.Source), AddedAt: r.AddedAt,
	}
}

func memberRef(sc domain.Scope) uuid.NullUUID {
	return uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil}
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
