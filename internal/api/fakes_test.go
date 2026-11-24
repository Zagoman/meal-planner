package api_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/service"
)

// fakePlanner is a small in-memory stand-in for package B's Plans and Lists.
// Every row remembers its household and every method filters by
// scope.HouseholdID exactly like the real services must, so these tests
// prove the API hands the services the token's scope (never an ID from the
// request) and maps their ErrNotFound to 404.
type fakePlanner struct {
	mu      sync.Mutex
	hub     *events.Hub
	current map[uuid.UUID]domain.Plan    // household -> active plan
	plans   map[uuid.UUID]uuid.UUID      // plan -> household
	meals   map[uuid.UUID]mealRow        // meal -> row
	items   map[uuid.UUID]itemRow        // item -> row
	calls   map[string]int               // method -> calls, to assert writes did not happen
	scopes  map[string][]domain.Scope    // method -> scopes seen
	swapped map[uuid.UUID]domain.Weekday // plan -> last swapped day
}

type mealRow struct {
	household uuid.UUID
	meal      domain.Meal
}

type itemRow struct {
	household uuid.UUID
	item      domain.ListItem
}

var (
	_ service.Plans = (*fakePlanner)(nil)
	_ service.Lists = (*fakePlanner)(nil)
)

func newFakePlanner(hub *events.Hub) *fakePlanner {
	return &fakePlanner{
		hub: hub, current: map[uuid.UUID]domain.Plan{}, plans: map[uuid.UUID]uuid.UUID{},
		meals: map[uuid.UUID]mealRow{}, items: map[uuid.UUID]itemRow{},
		calls: map[string]int{}, scopes: map[string][]domain.Scope{}, swapped: map[uuid.UUID]domain.Weekday{},
	}
}

func (f *fakePlanner) record(method string, s domain.Scope) {
	f.calls[method]++
	f.scopes[method] = append(f.scopes[method], s)
}

// seed registers a household's active plan with one meal (IDs may come from
// the real database so Feedback works on them) and one list item.
func (f *fakePlanner) seed(household, planID, mealID uuid.UUID, prompt, itemName string) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	meal := domain.Meal{ID: mealID, Day: domain.Monday, Title: "Seeded " + prompt, Protein: domain.ProteinFish,
		Minutes: 30, Servings: 2, Ingredients: []domain.Ingredient{}, Steps: []domain.Step{}, AfterBabyPortion: []string{}}
	f.current[household] = domain.Plan{ID: planID, Status: domain.PlanActive, Prompt: prompt, Meals: []domain.Meal{meal}}
	f.plans[planID] = household
	f.meals[mealID] = mealRow{household, meal}
	item := domain.ListItem{ID: uuid.New(), PlanID: planID, Name: itemName, Section: "other"}
	f.items[item.ID] = itemRow{household, item}
	return item.ID
}

func (f *fakePlanner) Generate(_ context.Context, s domain.Scope, prompt string) (domain.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Generate", s)
	p := domain.Plan{ID: uuid.New(), Status: domain.PlanActive, Prompt: prompt, CreatedAt: time.Now(), Meals: []domain.Meal{}}
	f.current[s.HouseholdID] = p
	f.plans[p.ID] = s.HouseholdID
	return p, nil
}

func (f *fakePlanner) Current(_ context.Context, s domain.Scope) (domain.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Current", s)
	p, ok := f.current[s.HouseholdID]
	if !ok {
		return domain.Plan{}, fmt.Errorf("current plan: %w", domain.ErrNotFound)
	}
	return p, nil
}

func (f *fakePlanner) SwapMeal(_ context.Context, s domain.Scope, planID uuid.UUID, day domain.Weekday, _ string) (domain.Meal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SwapMeal", s)
	if f.plans[planID] != s.HouseholdID {
		return domain.Meal{}, fmt.Errorf("plan: %w", domain.ErrNotFound)
	}
	f.swapped[planID] = day
	return domain.Meal{ID: uuid.New(), Day: day, Title: "Swapped"}, nil
}

func (f *fakePlanner) Substitute(_ context.Context, s domain.Scope, mealID uuid.UUID, from, to string) (domain.Meal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Substitute", s)
	row, ok := f.meals[mealID]
	if !ok || row.household != s.HouseholdID {
		return domain.Meal{}, fmt.Errorf("meal: %w", domain.ErrNotFound)
	}
	row.meal.Title += " (" + from + "->" + to + ")"
	f.meals[mealID] = row
	return row.meal, nil
}

func (f *fakePlanner) Meal(_ context.Context, s domain.Scope, mealID uuid.UUID) (domain.Meal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Meal", s)
	row, ok := f.meals[mealID]
	if !ok || row.household != s.HouseholdID {
		return domain.Meal{}, fmt.Errorf("meal: %w", domain.ErrNotFound)
	}
	return row.meal, nil
}

func (f *fakePlanner) Active(_ context.Context, s domain.Scope) (domain.List, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("Active", s)
	p, ok := f.current[s.HouseholdID]
	if !ok {
		return domain.List{}, fmt.Errorf("active list: %w", domain.ErrNotFound)
	}
	sec := domain.ListSection{Key: "other", Name: "Other", Items: []domain.ListItem{}}
	for _, r := range f.items {
		if r.household == s.HouseholdID && r.item.PlanID == p.ID {
			sec.Items = append(sec.Items, r.item)
		}
	}
	return domain.List{PlanID: p.ID, CheckHome: []domain.ListItem{}, Sections: []domain.ListSection{sec}}, nil
}

func (f *fakePlanner) Rebuild(context.Context, domain.Scope, uuid.UUID) (domain.List, error) {
	return domain.List{}, domain.ErrNotImplemented
}

func (f *fakePlanner) AddItem(_ context.Context, s domain.Scope, name, section string) (domain.ListItem, error) {
	f.mu.Lock()
	p, ok := f.current[s.HouseholdID]
	f.record("AddItem", s)
	if !ok {
		f.mu.Unlock()
		return domain.ListItem{}, fmt.Errorf("active list: %w", domain.ErrNotFound)
	}
	it := domain.ListItem{ID: uuid.New(), PlanID: p.ID, Name: name, Section: section, IsManual: true}
	f.items[it.ID] = itemRow{s.HouseholdID, it}
	f.mu.Unlock()
	f.hub.Publish(s.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: it.ID})
	return it, nil
}

func (f *fakePlanner) SetHaveIt(context.Context, domain.Scope, uuid.UUID, *bool) (domain.ListItem, error) {
	return domain.ListItem{}, domain.ErrNotImplemented
}

func (f *fakePlanner) SetChecked(_ context.Context, s domain.Scope, itemID uuid.UUID, checked bool) (domain.ListItem, error) {
	f.mu.Lock()
	f.record("SetChecked", s)
	row, ok := f.items[itemID]
	if !ok || row.household != s.HouseholdID {
		f.mu.Unlock()
		return domain.ListItem{}, fmt.Errorf("item: %w", domain.ErrNotFound)
	}
	row.item.Checked = checked
	f.items[itemID] = row
	f.mu.Unlock()
	f.hub.Publish(s.HouseholdID, events.Event{Kind: events.ItemChanged, ItemID: itemID})
	return row.item, nil
}

func (f *fakePlanner) DeleteItem(_ context.Context, s domain.Scope, itemID uuid.UUID) error {
	f.mu.Lock()
	f.record("DeleteItem", s)
	row, ok := f.items[itemID]
	if !ok || row.household != s.HouseholdID {
		f.mu.Unlock()
		return fmt.Errorf("item: %w", domain.ErrNotFound)
	}
	delete(f.items, itemID)
	f.mu.Unlock()
	f.hub.Publish(s.HouseholdID, events.Event{Kind: events.ItemDeleted, ItemID: itemID})
	return nil
}

func (f *fakePlanner) item(id uuid.UUID) (domain.ListItem, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.items[id]
	return r.item, ok
}

func (f *fakePlanner) itemCount(household uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.items {
		if r.household == household {
			n++
		}
	}
	return n
}

func (f *fakePlanner) currentPlan(household uuid.UUID) domain.Plan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current[household]
}

func (f *fakePlanner) mealTitle(id uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meals[id].meal.Title
}

func (f *fakePlanner) swappedDay(planID uuid.UUID) domain.Weekday {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.swapped[planID]
}
