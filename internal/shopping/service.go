package shopping

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/db"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/service"
)

// Service implements service.Lists.
type Service struct {
	pool   *pgxpool.Pool
	events *events.Hub
}

var _ service.Lists = (*Service)(nil)

// New returns the Lists service. hub may be nil (no live updates).
func New(pool *pgxpool.Pool, hub *events.Hub) *Service {
	return &Service{pool: pool, events: hub}
}

func (s *Service) publish(householdID uuid.UUID, e events.Event) {
	if s.events != nil {
		s.events.Publish(householdID, e)
	}
}

// Active returns the active plan's list, grouped by section.
func (s *Service) Active(ctx context.Context, sc domain.Scope) (domain.List, error) {
	q := db.New(s.pool)
	plan, err := q.GetActivePlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.List{}, notFound("active plan", err)
	}
	return LoadList(ctx, q, sc.HouseholdID, plan.ID)
}

// Rebuild regenerates the generated items of the plan's list from its meals,
// keeping ticks and manual items.
func (s *Service) Rebuild(ctx context.Context, sc domain.Scope, planID uuid.UUID) (domain.List, error) {
	var list domain.List
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(s.pool).WithTx(tx)
		if _, err := q.GetPlan(ctx, db.GetPlanParams{ID: planID, HouseholdID: sc.HouseholdID}); err != nil {
			return notFound("plan", err)
		}
		if err := RebuildItems(ctx, q, sc.HouseholdID, planID); err != nil {
			return err
		}
		var err error
		list, err = LoadList(ctx, q, sc.HouseholdID, planID)
		return err
	})
	if err != nil {
		return domain.List{}, err
	}
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	return list, nil
}

// AddItem adds a manual item to the active plan's list. An empty section
// means the household's last section (normally "other").
func (s *Service) AddItem(ctx context.Context, sc domain.Scope, name, section string) (domain.ListItem, error) {
	name = strings.TrimSpace(name)
	section = strings.TrimSpace(section)
	var problems []string
	if name == "" || Normalize(name) == "" {
		problems = append(problems, "name is required")
	}
	if len(name) > 200 {
		problems = append(problems, "name is longer than 200 characters")
	}
	q := db.New(s.pool)
	rows, err := q.ListSections(ctx, sc.HouseholdID)
	if err != nil {
		return domain.ListItem{}, fmt.Errorf("list sections: %w", err)
	}
	sections := sectionsFromDB(rows)
	if section == "" && len(sections) > 0 {
		section = sortedSections(sections)[len(sections)-1].Key
	}
	if !validSection(section, sections) {
		problems = append(problems, fmt.Sprintf("unknown section %q", section))
	}
	if len(problems) > 0 {
		return domain.ListItem{}, &domain.ValidationError{Problems: problems}
	}
	plan, err := q.GetActivePlan(ctx, sc.HouseholdID)
	if err != nil {
		return domain.ListItem{}, notFound("active plan", err)
	}
	row, err := q.AddManualListItem(ctx, db.AddManualListItemParams{
		HouseholdID: sc.HouseholdID, PlanID: plan.ID, Name: name,
		NormalizedName: Normalize(name), Section: section,
	})
	if err != nil {
		return domain.ListItem{}, fmt.Errorf("add list item: %w", err)
	}
	item := ItemFromDB(row)
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: item.ID})
	return item, nil
}

// SetChecked ticks or unticks an item and records who did it.
func (s *Service) SetChecked(ctx context.Context, sc domain.Scope, itemID uuid.UUID, checked bool) (domain.ListItem, error) {
	row, err := db.New(s.pool).SetListItemChecked(ctx, db.SetListItemCheckedParams{
		Checked:     checked,
		CheckedBy:   uuid.NullUUID{UUID: sc.MemberID, Valid: sc.MemberID != uuid.Nil},
		ID:          itemID,
		HouseholdID: sc.HouseholdID,
	})
	if err != nil {
		return domain.ListItem{}, notFound("list item", err)
	}
	item := ItemFromDB(row)
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: item.ID})
	return item, nil
}

// DeleteItem removes an item. A deleted generated item comes back on the
// next rebuild if a meal still needs it.
func (s *Service) DeleteItem(ctx context.Context, sc domain.Scope, itemID uuid.UUID) error {
	n, err := db.New(s.pool).DeleteListItem(ctx, db.DeleteListItemParams{ID: itemID, HouseholdID: sc.HouseholdID})
	if err != nil {
		return fmt.Errorf("delete list item: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete list item: %w", domain.ErrNotFound)
	}
	s.publish(sc.HouseholdID, events.Event{Kind: events.ItemDeleted, ItemID: itemID})
	return nil
}

// RebuildItems recomputes the generated items of a plan inside the caller's
// transaction: items are upserted by normalized name (ticks survive), stale
// generated items are removed and manual items are never touched. The caller
// publishes events.ListRebuilt after commit. The plan must belong to
// householdID (every query filters by it).
func RebuildItems(ctx context.Context, q *db.Queries, householdID, planID uuid.UUID) error {
	ings, err := q.ListIngredientsForPlan(ctx, db.ListIngredientsForPlanParams{PlanID: planID, HouseholdID: householdID})
	if err != nil {
		return fmt.Errorf("list ingredients: %w", err)
	}
	secRows, err := q.ListSections(ctx, householdID)
	if err != nil {
		return fmt.Errorf("list sections: %w", err)
	}
	pantryRows, err := q.ListPantryItems(ctx, householdID)
	if err != nil {
		return fmt.Errorf("list pantry: %w", err)
	}
	pantry := make([]string, len(pantryRows))
	for i, p := range pantryRows {
		pantry[i] = p.NormalizedName
	}
	domIngs := make([]domain.Ingredient, len(ings))
	for i, in := range ings {
		domIngs[i] = IngredientFromDB(in)
	}
	items := Build(domIngs, sectionsFromDB(secRows), pantry)
	keep := make([]string, 0, len(items))
	for _, it := range items {
		if _, err := q.UpsertGeneratedListItem(ctx, db.UpsertGeneratedListItemParams{
			HouseholdID: householdID, PlanID: planID, Name: it.Name, NormalizedName: it.NormalizedName,
			QtyText: it.QtyText, NeedText: it.NeedText, Section: it.Section,
		}); err != nil {
			return fmt.Errorf("upsert list item: %w", err)
		}
		keep = append(keep, it.NormalizedName)
	}
	if err := q.DeleteStaleGeneratedListItems(ctx, db.DeleteStaleGeneratedListItemsParams{
		PlanID: planID, HouseholdID: householdID, Keep: keep,
	}); err != nil {
		return fmt.Errorf("delete stale list items: %w", err)
	}
	return nil
}

// LoadList reads and groups a plan's list.
func LoadList(ctx context.Context, q *db.Queries, householdID, planID uuid.UUID) (domain.List, error) {
	rows, err := q.ListItemsForPlan(ctx, db.ListItemsForPlanParams{PlanID: planID, HouseholdID: householdID})
	if err != nil {
		return domain.List{}, fmt.Errorf("list items: %w", err)
	}
	secRows, err := q.ListSections(ctx, householdID)
	if err != nil {
		return domain.List{}, fmt.Errorf("list sections: %w", err)
	}
	items := make([]domain.ListItem, len(rows))
	for i, r := range rows {
		items[i] = ItemFromDB(r)
	}
	return Group(planID, items, sectionsFromDB(secRows)), nil
}

func validSection(key string, sections []domain.Section) bool {
	if key == domain.PantryCheckSection {
		return true
	}
	for _, s := range sections {
		if s.Key == key {
			return true
		}
	}
	return false
}

func sectionsFromDB(rows []db.StoreSection) []domain.Section {
	out := make([]domain.Section, len(rows))
	for i, r := range rows {
		out[i] = domain.Section{Key: r.Key, Name: r.Name, SortOrder: int(r.SortOrder)}
	}
	return out
}

// ItemFromDB converts a stored list item.
func ItemFromDB(r db.ListItem) domain.ListItem {
	it := domain.ListItem{
		ID: r.ID, PlanID: r.PlanID, Name: r.Name, QtyText: r.QtyText, NeedText: r.NeedText,
		Section: r.Section, Checked: r.Checked, IsManual: r.IsManual,
	}
	if r.CheckedBy.Valid {
		id := r.CheckedBy.UUID
		it.CheckedBy = &id
	}
	return it
}

// IngredientFromDB converts a stored ingredient.
func IngredientFromDB(r db.Ingredient) domain.Ingredient {
	return domain.Ingredient{
		ID: r.ID, Name: r.Name, NormalizedName: r.NormalizedName, Qty: r.Qty, Unit: r.Unit, Section: r.Section,
	}
}

func notFound(what string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get %s: %w", what, domain.ErrNotFound)
	}
	return fmt.Errorf("get %s: %w", what, err)
}
