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

var (
	_ service.Outcomes = (*Store)(nil)
	_ service.Library  = (*Store)(nil)
)

// libraryState is a household's outcomes and recipe library in the fake.
// Recipes are found by title: every meal title the fake planned is one.
type libraryState struct {
	outcomes map[uuid.UUID]domain.MealOutcome // by meal
	recipes  []*libRecipe
	photos   map[uuid.UUID]domain.RecipePhoto // by recipe
}

type libRecipe struct {
	domain.Recipe
	tweak     *domain.Job
	tweakedID *uuid.UUID
}

func (h *household) lib() *libraryState {
	if h.library == nil {
		h.library = &libraryState{outcomes: map[uuid.UUID]domain.MealOutcome{}}
	}
	return h.library
}

// syncLibrary adds every planned meal's title to the library. Callers hold s.mu.
func (h *household) syncLibrary() {
	l := h.lib()
	for _, m := range h.meals {
		key := domain.TitleKey(m.Title)
		if slices.ContainsFunc(l.recipes, func(r *libRecipe) bool { return r.ParentID == nil && domain.TitleKey(r.Meal.Title) == key }) {
			continue
		}
		meal := m
		meal.Day = ""
		l.recipes = append(l.recipes, &libRecipe{Recipe: domain.Recipe{ID: uuid.New(), Source: domain.SourceClaude, Meal: meal, CreatedAt: time.Now()}})
	}
	slices.SortFunc(l.recipes, func(a, b *libRecipe) int { return strings.Compare(a.Meal.Title, b.Meal.Title) })
}

func (h *household) recipeByID(id uuid.UUID) (*libRecipe, error) {
	h.syncLibrary()
	for _, r := range h.lib().recipes {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("recipe: %w", domain.ErrNotFound)
}

func (h *household) stats(title string) domain.RecipeStats {
	var st domain.RecipeStats
	sum := 0
	for mealID, o := range h.lib().outcomes {
		m, ok := h.meals[mealID]
		if !ok || domain.TitleKey(m.Title) != domain.TitleKey(title) {
			continue
		}
		if o.Status == domain.Cooked {
			st.TimesCooked++
		}
		if o.Rating > 0 {
			st.Ratings++
			sum += o.Rating
		}
		if o.Note != "" {
			st.LatestNote = o.Note
		}
	}
	if st.Ratings > 0 {
		st.Rating = float64(sum) / float64(st.Ratings)
	}
	return st
}

// ---- Outcomes ------------------------------------------------------------------

func (s *Store) RateList(_ context.Context, sc domain.Scope) (domain.RateList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.RateList{}, err
	}
	if h.plan == nil {
		return domain.RateList{}, fmt.Errorf("rate list: %w", domain.ErrNotFound)
	}
	return h.rateList(), nil
}

func (h *household) rateList() domain.RateList {
	l := domain.RateList{Plan: clonePlan(*h.plan)}
	for _, m := range h.plan.Meals {
		rm := domain.RateMeal{Meal: m}
		if o, ok := h.lib().outcomes[m.ID]; ok {
			rm.Outcome = &o
		}
		l.Meals = append(l.Meals, rm)
	}
	return l
}

func (s *Store) SetOutcome(_ context.Context, sc domain.Scope, mealID uuid.UUID, in domain.OutcomeInput) (domain.RateMeal, error) {
	if in.Rating < 0 || in.Rating > 5 {
		return domain.RateMeal{}, &domain.ValidationError{Problems: []string{"rating: 1 to 5"}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.RateMeal{}, err
	}
	m, ok := h.meals[mealID]
	if !ok {
		return domain.RateMeal{}, fmt.Errorf("outcome: meal %w", domain.ErrNotFound)
	}
	l := h.lib()
	old, hasOld := l.outcomes[mealID]
	o := domain.MealOutcome{MealID: mealID, Note: old.Note, At: time.Now()}
	if in.Note != nil {
		o.Note = strings.TrimSpace(*in.Note)
	}
	switch {
	case in.Clear:
		delete(l.outcomes, mealID)
		return domain.RateMeal{Meal: m}, nil
	case in.NotCooked:
		o.Status = domain.NotCooked
	case in.Rating > 0:
		o.Status, o.Rating = domain.Cooked, in.Rating
	case hasOld:
		o.Status, o.Rating = old.Status, old.Rating
	default:
		return domain.RateMeal{}, &domain.ValidationError{Problems: []string{"rate the meal or mark it not cooked first"}}
	}
	l.outcomes[mealID] = o
	return domain.RateMeal{Meal: m, Outcome: &o}, nil
}

// ---- Library --------------------------------------------------------------------

func (s *Store) Search(_ context.Context, sc domain.Scope, q string, filter domain.RecipeFilter) ([]domain.RecipeSummary, error) {
	if filter != "" && filter != domain.FilterAll && filter != domain.FilterRated4 && filter != domain.FilterOwn && !domain.MealType(filter).Valid() {
		return nil, &domain.ValidationError{Problems: []string{"unknown filter"}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	h.syncLibrary()
	q = strings.ToLower(strings.TrimSpace(q))
	out := []domain.RecipeSummary{}
	for _, r := range h.lib().recipes {
		sum := domain.RecipeSummary{
			ID: r.ID, Title: r.Meal.Title, MealType: r.Meal.MealType, Minutes: r.Meal.Minutes, Source: r.Source,
			Tweaked: r.ParentID != nil, RecipeStats: h.stats(r.Meal.Title),
		}
		match := q == "" || strings.Contains(strings.ToLower(sum.Title), q) ||
			slices.ContainsFunc(r.Meal.Ingredients, func(i domain.Ingredient) bool { return strings.Contains(strings.ToLower(i.Name), q) })
		switch {
		case !match:
		case filter == domain.FilterRated4 && sum.Rating < 4:
		case filter == domain.FilterOwn && sum.Source == domain.SourceClaude:
		case domain.MealType(filter).Valid() && sum.MealType != domain.MealType(filter):
		default:
			out = append(out, sum)
		}
	}
	return out, nil
}

func (s *Store) RecipeDetail(_ context.Context, sc domain.Scope, recipeID uuid.UUID) (domain.RecipeDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.RecipeDetail{}, err
	}
	r, err := h.recipeByID(recipeID)
	if err != nil {
		return domain.RecipeDetail{}, err
	}
	d := domain.RecipeDetail{Recipe: r.Recipe, RecipeStats: h.stats(r.Meal.Title), Tweak: r.tweak, TweakedID: r.tweakedID}
	if p, ok := h.lib().photos[recipeID]; ok {
		at := p.UpdatedAt
		d.PhotoAt = &at
	}
	return d, nil
}

// RequestTweak makes "<title> (tweaked)"; a prompt containing "fail" fails.
func (s *Store) RequestTweak(_ context.Context, sc domain.Scope, recipeID uuid.UUID, prompt string) (domain.Job, error) {
	if strings.TrimSpace(prompt) == "" {
		return domain.Job{}, &domain.ValidationError{Problems: []string{"prompt: say what to change"}}
	}
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.Job{}, err
	}
	r, err := h.recipeByID(recipeID)
	if err != nil {
		s.mu.Unlock()
		return domain.Job{}, err
	}
	job := domain.Job{ID: uuid.New(), Kind: domain.JobTweak, Status: domain.JobQueued, CreatedAt: time.Now()}
	r.tweak = &job
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.RecipeUpdated, RecipeID: recipeID})
	s.later(func() {
		s.mu.Lock()
		done := *r.tweak
		if strings.Contains(strings.ToLower(prompt), "fail") {
			done.Status, done.Error = domain.JobFailed, "claude: overloaded"
		} else {
			done.Status = domain.JobDone
			parent := r.ID
			meal := r.Meal
			meal.Title += " (tweaked)"
			v := &libRecipe{Recipe: domain.Recipe{ID: uuid.New(), Source: r.Source, ParentID: &parent, Meal: meal, CreatedAt: time.Now()}}
			h.lib().recipes = append(h.lib().recipes, v)
			r.tweakedID = &v.ID
		}
		r.tweak = &done
		s.mu.Unlock()
		s.publish(sc.HouseholdID, events.Event{Kind: events.RecipeUpdated, RecipeID: recipeID})
	})
	return job, nil
}

func (s *Store) AddToNextWeek(ctx context.Context, sc domain.Scope, recipeID uuid.UUID, from domain.MealSource) (domain.DraftMeal, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.DraftMeal{}, err
	}
	r, err := h.recipeByID(recipeID)
	if err != nil {
		s.mu.Unlock()
		return domain.DraftMeal{}, err
	}
	d := h.drafts()
	if d.plan == nil {
		d.plan = &domain.Plan{ID: uuid.New(), WeekStart: weekStart(time.Now()), Status: domain.PlanDraft, CreatedAt: time.Now()}
	}
	m := r.Meal
	m.ID, m.AddedFrom, m.Day = uuid.New(), from, ""
	h.meals[m.ID] = m
	d.plan.Meals = append(d.plan.Meals, m)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.PlanUpdated})
	return s.DraftMeal(ctx, sc, m.ID)
}

func (s *Store) AddToThisWeek(_ context.Context, sc domain.Scope, recipeID uuid.UUID) (int, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	r, err := h.recipeByID(recipeID)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	if h.plan == nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("there is no plan this week to add to: %w", domain.ErrConflict)
	}
	have := map[string]bool{}
	for _, it := range h.items {
		have[normalize(it.Name)] = true
	}
	n := 0
	for _, it := range shopping.Build(r.Meal.Ingredients, h.sections, nil) {
		if have[it.NormalizedName] {
			continue
		}
		h.items = append(h.items, domain.ListItem{ID: uuid.New(), PlanID: h.plan.ID, Name: it.Name, QtyText: it.QtyText, Section: it.Section, IsManual: true})
		n++
	}
	s.mu.Unlock()
	if n > 0 {
		s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	}
	return n, nil
}

// notCooked lists the active plan's not-cooked meals whose recipe is not in
// the draft. Callers hold s.mu.
func (h *household) notCooked() []domain.NotCookedMeal {
	out := []domain.NotCookedMeal{}
	if h.plan == nil {
		return out
	}
	h.syncLibrary()
	inDraft := map[string]bool{}
	if d := h.drafts(); d.plan != nil {
		for _, m := range d.plan.Meals {
			inDraft[domain.TitleKey(m.Title)] = true
		}
	}
	for _, m := range h.plan.Meals {
		o, ok := h.lib().outcomes[m.ID]
		if !ok || o.Status != domain.NotCooked || inDraft[domain.TitleKey(m.Title)] {
			continue
		}
		for _, r := range h.lib().recipes {
			if r.ParentID == nil && domain.TitleKey(r.Meal.Title) == domain.TitleKey(m.Title) {
				out = append(out, domain.NotCookedMeal{RecipeID: r.ID, Title: m.Title, MealType: m.MealType})
			}
		}
	}
	return out
}

// rateFirst reports whether the active plan has unanswered meals.
func (h *household) rateFirst() bool {
	if h.plan == nil {
		return false
	}
	for _, m := range h.plan.Meals {
		if _, ok := h.lib().outcomes[m.ID]; !ok {
			return true
		}
	}
	return false
}
