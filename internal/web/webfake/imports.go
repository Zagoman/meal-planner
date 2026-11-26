package webfake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
)

// fakeDraft is an import in the fake: "reading" it takes GenerateDelay.
// Text becomes a recipe line by line (title, then "- " ingredients, then
// steps); a link containing "fail" fails like an unreachable page.
type fakeDraft struct {
	d domain.RecipeDraft
}

func (s *Store) Import(_ context.Context, sc domain.Scope, in domain.ImportInput) (domain.RecipeDraft, error) {
	if in.Source == domain.ImportText && strings.TrimSpace(in.Text) == "" {
		return domain.RecipeDraft{}, &domain.ValidationError{Problems: []string{"text: paste the recipe"}}
	}
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return domain.RecipeDraft{}, err
	}
	if s.imports == nil {
		s.imports = map[uuid.UUID]*fakeDraft{}
	}
	job := domain.Job{ID: uuid.New(), Kind: domain.JobImport, Status: domain.JobQueued, CreatedAt: time.Now()}
	fd := &fakeDraft{d: domain.RecipeDraft{ID: uuid.New(), Source: in.Source, RawInput: in.Text, Uncertain: []string{}, Job: &job}}
	s.imports[fd.d.ID] = fd
	h.importIDs = append(h.importIDs, fd.d.ID)
	out := fd.d
	s.mu.Unlock()
	s.later(func() {
		s.mu.Lock()
		done := *fd.d.Job
		if in.Source == domain.ImportLink && strings.Contains(in.Text, "fail") {
			done.Status, done.Error = domain.JobFailed, "couldn't open that page: fetch: the page answered 404"
		} else {
			done.Status = domain.JobDone
			r, unc := parseFakeRecipe(in)
			fd.d.Recipe, fd.d.Uncertain = &r, unc
		}
		fd.d.Job = &done
		s.mu.Unlock()
		s.publish(sc.HouseholdID, events.Event{Kind: events.RecipeUpdated, RecipeID: fd.d.ID})
	})
	return out, nil
}

func parseFakeRecipe(in domain.ImportInput) (domain.MealJSON, []string) {
	r := domain.MealJSON{MealType: domain.MealDinner, Protein: domain.ProteinOther, Minutes: 30, Servings: 4, AfterBabyPortion: []string{}}
	switch in.Source {
	case domain.ImportPhoto:
		r.Title = "Recipe from a photo"
		r.Ingredients = []domain.IngredientJSON{{Name: "Rye flour", NormalizedName: "rye flour", Qty: 0, Unit: "g", Section: "dry_goods"}}
		r.Steps = []domain.StepJSON{{Text: "Mix and bake."}}
		return r, []string{"ingredients[0].qty"}
	case domain.ImportLink:
		r.Title = "Recipe from a link"
		r.Ingredients = []domain.IngredientJSON{{Name: "Cod fillet", NormalizedName: "cod fillet", Qty: 500, Unit: "g", Section: "meat_fish"}}
		r.Steps = []domain.StepJSON{{Text: "Bake the fish."}}
		return r, []string{}
	}
	for i, line := range strings.Split(in.Text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case i == 0:
			r.Title = line
		case strings.HasPrefix(line, "- "):
			name := strings.TrimPrefix(line, "- ")
			r.Ingredients = append(r.Ingredients, domain.IngredientJSON{Name: name, NormalizedName: strings.ToLower(name), Qty: 1, Unit: "pcs", Section: "other"})
		default:
			r.Steps = append(r.Steps, domain.StepJSON{Text: line})
		}
	}
	return r, []string{"minutes"}
}

func (s *Store) ImportDraft(_ context.Context, sc domain.Scope, id uuid.UUID) (domain.RecipeDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.RecipeDraft{}, err
	}
	fd, ok := s.imports[id]
	if !ok || !containsID(h.importIDs, id) {
		return domain.RecipeDraft{}, fmt.Errorf("draft: %w", domain.ErrNotFound)
	}
	return fd.d, nil
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func (s *Store) DiscardImport(ctx context.Context, sc domain.Scope, id uuid.UUID) error {
	if _, err := s.ImportDraft(ctx, sc, id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.imports, id)
	s.mu.Unlock()
	return nil
}

func (s *Store) SaveImport(ctx context.Context, sc domain.Scope, id uuid.UUID, r domain.MealJSON) (domain.Recipe, error) {
	d, err := s.ImportDraft(ctx, sc, id)
	if err != nil {
		return domain.Recipe{}, err
	}
	rec, err := s.addOwn(sc, r, domain.RecipeSource(d.Source))
	if err == nil {
		s.mu.Lock()
		delete(s.imports, id)
		s.mu.Unlock()
	}
	return rec, err
}

func (s *Store) CreateRecipe(_ context.Context, sc domain.Scope, r domain.MealJSON) (domain.Recipe, error) {
	return s.addOwn(sc, r, domain.SourceManual)
}

func (s *Store) addOwn(sc domain.Scope, r domain.MealJSON, source domain.RecipeSource) (domain.Recipe, error) {
	var problems []string
	if strings.TrimSpace(r.Title) == "" {
		problems = append(problems, "title: 1 to 200 characters")
	}
	if r.Minutes < 1 {
		problems = append(problems, "minutes: 1 to 1440")
	}
	if r.Servings < 1 {
		problems = append(problems, "serves: 1 to 50")
	}
	var ings []domain.Ingredient
	for _, in := range r.Ingredients {
		if strings.TrimSpace(in.Name) == "" {
			continue
		}
		if in.Qty < 0 {
			problems = append(problems, fmt.Sprintf("ingredient %s: amount 0 or more", in.Name))
		}
		ings = append(ings, domain.Ingredient{ID: uuid.New(), Name: in.Name, NormalizedName: strings.ToLower(in.Name), Qty: in.Qty, Unit: in.Unit, Section: in.Section})
	}
	if len(ings) == 0 {
		problems = append(problems, "ingredients: at least one")
	}
	var steps []domain.Step
	for _, st := range r.Steps {
		if strings.TrimSpace(st.Text) != "" {
			steps = append(steps, domain.Step(st))
		}
	}
	if len(steps) == 0 {
		problems = append(problems, "steps: at least one")
	}
	if len(problems) > 0 {
		return domain.Recipe{}, &domain.ValidationError{Problems: problems}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.Recipe{}, err
	}
	rec := domain.Recipe{ID: uuid.New(), Source: source, InLibrary: true, CreatedAt: time.Now(), Meal: domain.Meal{
		MealType: r.MealType, Title: r.Title, Protein: r.Protein, Minutes: r.Minutes, OnePot: r.OnePot, Servings: r.Servings,
		Ingredients: ings, Steps: steps, AfterBabyPortion: []string{},
	}}
	h.lib().recipes = append(h.lib().recipes, &libRecipe{Recipe: rec})
	return rec, nil
}

// TickAt applies an offline tick unless a later tick of the item was
// applied already.
func (s *Store) TickAt(ctx context.Context, sc domain.Scope, itemID uuid.UUID, checked bool, at time.Time) (domain.ListItem, error) {
	s.mu.Lock()
	if s.tickAt == nil {
		s.tickAt = map[uuid.UUID]time.Time{}
	}
	last, seen := s.tickAt[itemID]
	if seen && at.Before(last) {
		h, err := s.hh(sc)
		if err != nil {
			s.mu.Unlock()
			return domain.ListItem{}, err
		}
		for _, it := range h.items {
			if it.ID == itemID {
				s.mu.Unlock()
				return it, nil
			}
		}
		s.mu.Unlock()
		return domain.ListItem{}, fmt.Errorf("tick: %w", domain.ErrNotFound)
	}
	s.mu.Unlock()
	it, err := s.SetChecked(ctx, sc, itemID, checked)
	if err == nil {
		s.mu.Lock()
		s.tickAt[itemID] = at
		s.mu.Unlock()
	}
	return it, err
}
