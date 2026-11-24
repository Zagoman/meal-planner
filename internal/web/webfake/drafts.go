package webfake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
)

var _ service.Drafts = (*Store)(nil)

// draftState is a household's next-week draft in the fake.
type draftState struct {
	plan     *domain.Plan
	making   bool
	failed   string
	votes    map[uuid.UUID]map[uuid.UUID]int // meal -> member -> vote
	swapping map[uuid.UUID]bool
	swapErr  map[uuid.UUID]string
	asking   bool
	askErr   string
}

func (h *household) drafts() *draftState {
	if h.draft == nil {
		h.draft = &draftState{
			votes: map[uuid.UUID]map[uuid.UUID]int{}, swapping: map[uuid.UUID]bool{}, swapErr: map[uuid.UUID]string{},
		}
	}
	return h.draft
}

// later runs a simulated Claude job: at once without GenerateDelay (web
// tests), else after it in the background (the e2e server).
func (s *Store) later(f func()) {
	if s.GenerateDelay <= 0 {
		f()
		return
	}
	go func() {
		time.Sleep(s.GenerateDelay)
		f()
	}()
}

func (s *Store) confirmsAt(h *household, week time.Time) time.Time {
	hh, mm := 8, 0
	_, _ = fmt.Sscanf(h.profile.AutoConfirmTime, "%d:%d", &hh, &mm)
	return time.Date(week.Year(), week.Month(), week.Day(), hh, mm, 0, 0, time.Local)
}

func (s *Store) Card(_ context.Context, sc domain.Scope) (domain.DraftCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.DraftCard{}, err
	}
	d := h.drafts()
	switch {
	case d.making:
		return domain.DraftCard{State: domain.DraftMaking}, nil
	case d.plan != nil:
		id := d.plan.ID
		return domain.DraftCard{State: domain.DraftReady, PlanID: &id, MealCount: len(d.plan.Meals), ConfirmsAt: s.confirmsAt(h, d.plan.WeekStart)}, nil
	case d.failed != "":
		return domain.DraftCard{State: domain.DraftFailed, Error: d.failed}, nil
	}
	return domain.DraftCard{State: domain.DraftNone}, nil
}

// RequestDraft makes a draft of the profile's dinner count. A household
// whose shop is "fail" fails, like a Claude error.
func (s *Store) RequestDraft(_ context.Context, sc domain.Scope) (domain.Job, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.Job{}, err
	}
	d := h.drafts()
	job := domain.Job{ID: uuid.New(), Kind: domain.JobDraft, Status: domain.JobQueued, CreatedAt: time.Now()}
	if d.making {
		s.mu.Unlock()
		return job, nil
	}
	d.making, d.failed = true, ""
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	s.later(func() {
		s.mu.Lock()
		d.making = false
		if h.profile.Shop == "fail" {
			d.failed = "claude: overloaded"
		} else {
			p := domain.Plan{ID: uuid.New(), WeekStart: weekStart(time.Now()), Status: domain.PlanDraft, CreatedAt: time.Now()}
			for i := range max(1, min(7, h.profile.MealTargets[domain.MealDinner])) {
				p.Meals = append(p.Meals, h.nextMeal(domain.Weekdays[i], nil))
			}
			d.plan = &p
			clear(d.votes)
			clear(d.swapping)
			clear(d.swapErr)
		}
		s.mu.Unlock()
		s.publish(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	})
	return job, nil
}

func (s *Store) draftMealLocked(h *household, sc domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error) {
	d := h.drafts()
	if d.plan == nil {
		return domain.DraftMeal{}, fmt.Errorf("draft meal: %w", domain.ErrNotFound)
	}
	i := slices.IndexFunc(d.plan.Meals, func(m domain.Meal) bool { return m.ID == mealID })
	if i < 0 {
		return domain.DraftMeal{}, fmt.Errorf("draft meal: %w", domain.ErrNotFound)
	}
	dm := domain.DraftMeal{Meal: d.plan.Meals[i], Votes: []domain.Vote{}, Swapping: d.swapping[mealID], SwapError: d.swapErr[mealID]}
	var members []uuid.UUID
	for id := range d.votes[mealID] {
		members = append(members, id)
	}
	slices.SortFunc(members, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range members {
		v := d.votes[mealID][id]
		name := ""
		if m := s.members[id]; m != nil {
			name = m.Name
		}
		dm.Votes = append(dm.Votes, domain.Vote{MemberID: id, Name: name, Vote: v})
		if id == sc.MemberID {
			dm.Mine = v
		}
	}
	return dm, nil
}

func (s *Store) Review(_ context.Context, sc domain.Scope) (domain.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Review{}, err
	}
	d := h.drafts()
	if d.plan == nil {
		return domain.Review{}, fmt.Errorf("review: %w", domain.ErrNotFound)
	}
	r := domain.Review{Plan: clonePlan(*d.plan), ConfirmsAt: s.confirmsAt(h, d.plan.WeekStart), Asking: d.asking, AskError: d.askErr}
	for _, m := range d.plan.Meals {
		dm, _ := s.draftMealLocked(h, sc, m.ID)
		r.Meals = append(r.Meals, dm)
	}
	return r, nil
}

func (s *Store) DraftMeal(_ context.Context, sc domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.DraftMeal{}, err
	}
	return s.draftMealLocked(h, sc, mealID)
}

func (s *Store) Vote(ctx context.Context, sc domain.Scope, mealID uuid.UUID, vote int) (domain.DraftMeal, error) {
	if vote < -1 || vote > 1 {
		return domain.DraftMeal{}, &domain.ValidationError{Problems: []string{"vote: 1, -1 or 0"}}
	}
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.DraftMeal{}, err
	}
	if _, err := s.draftMealLocked(h, sc, mealID); err != nil {
		s.mu.Unlock()
		return domain.DraftMeal{}, err
	}
	d := h.drafts()
	if d.votes[mealID] == nil {
		d.votes[mealID] = map[uuid.UUID]int{}
	}
	if vote == 0 {
		delete(d.votes[mealID], sc.MemberID)
	} else {
		d.votes[mealID][sc.MemberID] = vote
	}
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	return s.DraftMeal(ctx, sc, mealID)
}

func (s *Store) RequestSwap(ctx context.Context, sc domain.Scope, mealID uuid.UUID) (domain.DraftMeal, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.DraftMeal{}, err
	}
	if _, err := s.draftMealLocked(h, sc, mealID); err != nil {
		s.mu.Unlock()
		return domain.DraftMeal{}, err
	}
	d := h.drafts()
	d.swapping[mealID] = true
	delete(d.swapErr, mealID)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	s.later(func() {
		s.mu.Lock()
		delete(d.swapping, mealID)
		if d.plan != nil {
			if i := slices.IndexFunc(d.plan.Meals, func(m domain.Meal) bool { return m.ID == mealID }); i >= 0 {
				exclude := map[string]bool{}
				for _, m := range d.plan.Meals {
					exclude[m.Title] = true
				}
				old := d.plan.Meals[i]
				m := h.nextMeal(old.Day, exclude)
				delete(h.meals, m.ID)
				m.ID, m.MealType = old.ID, old.MealType
				h.meals[m.ID] = m
				d.plan.Meals[i] = m
				delete(d.votes, mealID)
			}
		}
		s.mu.Unlock()
		s.publish(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	})
	return s.DraftMeal(ctx, sc, mealID)
}

func (s *Store) RemoveMeal(_ context.Context, sc domain.Scope, mealID uuid.UUID) error {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if _, err := s.draftMealLocked(h, sc, mealID); err != nil {
		s.mu.Unlock()
		return err
	}
	d := h.drafts()
	d.plan.Meals = slices.DeleteFunc(d.plan.Meals, func(m domain.Meal) bool { return m.ID == mealID })
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.MealUpdated, MealID: mealID})
	return nil
}

// RequestAsk replaces the last meal; a prompt containing "fail" fails.
func (s *Store) RequestAsk(_ context.Context, sc domain.Scope, prompt string) (domain.Job, error) {
	if strings.TrimSpace(prompt) == "" {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"prompt: say what to change"}}
	}
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.Job{}, err
	}
	d := h.drafts()
	if d.plan == nil {
		s.mu.Unlock()
		return domain.Job{}, fmt.Errorf("ask: %w", domain.ErrNotFound)
	}
	d.asking, d.askErr = true, ""
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	s.later(func() {
		s.mu.Lock()
		d.asking = false
		switch {
		case strings.Contains(strings.ToLower(prompt), "fail"):
			d.askErr = "claude: answer still invalid after retry"
		case d.plan != nil && len(d.plan.Meals) > 0:
			last := len(d.plan.Meals) - 1
			exclude := map[string]bool{}
			for _, m := range d.plan.Meals {
				exclude[m.Title] = true
			}
			d.plan.Meals[last] = h.nextMeal(d.plan.Meals[last].Day, exclude)
		}
		s.mu.Unlock()
		s.publish(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	})
	return domain.Job{ID: uuid.New(), Kind: domain.JobAsk, Status: domain.JobQueued, CreatedAt: time.Now()}, nil
}

func (s *Store) ListPreview(_ context.Context, sc domain.Scope) (domain.List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.List{}, err
	}
	d := h.drafts()
	if d.plan == nil {
		return domain.List{}, fmt.Errorf("preview: %w", domain.ErrNotFound)
	}
	var ings []domain.Ingredient
	for _, m := range d.plan.Meals {
		ings = append(ings, m.Ingredients...)
	}
	pantry := make([]string, len(h.pantry))
	for i, p := range h.pantry {
		pantry[i] = p.NormalizedName
	}
	var items []domain.ListItem
	for _, it := range shopping.Build(ings, h.sections, pantry) {
		items = append(items, domain.ListItem{Name: it.Name, QtyText: it.QtyText, NeedText: it.NeedText, Section: it.Section, CheckHome: it.CheckHome})
	}
	return shopping.Group(d.plan.ID, items, h.sections), nil
}

// Confirm makes the draft the active plan with a fresh list; without a
// draft it returns the current plan, like the real service.
func (s *Store) Confirm(ctx context.Context, sc domain.Scope) (domain.Plan, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.Plan{}, err
	}
	d := h.drafts()
	if d.plan == nil {
		s.mu.Unlock()
		return s.Current(ctx, sc)
	}
	p := *d.plan
	p.Status = domain.PlanActive
	now := time.Now()
	p.ConfirmedAt = &now
	h.plan = &p
	d.plan = nil
	h.items = slices.DeleteFunc(h.items, func(it domain.ListItem) bool { return it.IsManual })
	h.rebuild(true)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	s.publish(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	return clonePlan(p), nil
}
