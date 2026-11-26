package webfake

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// recipeFor is the library recipe a planned meal is saved as. Callers hold s.mu.
func (h *household) recipeFor(title string) *libRecipe {
	h.syncLibrary()
	key := domain.TitleKey(title)
	for _, r := range h.lib().recipes {
		if r.ParentID == nil && domain.TitleKey(r.Meal.Title) == key {
			return r
		}
	}
	return nil
}

func (s *Store) SetPhoto(_ context.Context, sc domain.Scope, recipeID uuid.UUID, jpeg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return err
	}
	if len(jpeg) == 0 {
		return &domain.ValidationError{Problems: []string{"photo: a picture"}}
	}
	if _, err := h.recipeByID(recipeID); err != nil {
		return err
	}
	l := h.lib()
	if l.photos == nil {
		l.photos = map[uuid.UUID]domain.RecipePhoto{}
	}
	l.photos[recipeID] = domain.RecipePhoto{JPEG: jpeg, UpdatedAt: time.Now()}
	return nil
}

func (s *Store) Photo(_ context.Context, sc domain.Scope, recipeID uuid.UUID) (domain.RecipePhoto, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return domain.RecipePhoto{}, err
	}
	p, ok := h.lib().photos[recipeID]
	if !ok {
		return domain.RecipePhoto{}, fmt.Errorf("photo: %w", domain.ErrNotFound)
	}
	return p, nil
}

func (s *Store) PhotoAt(_ context.Context, sc domain.Scope, recipeID uuid.UUID) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return nil, err
	}
	p, ok := h.lib().photos[recipeID]
	if !ok {
		return nil, nil
	}
	return &p.UpdatedAt, nil
}

func (s *Store) DeletePhoto(_ context.Context, sc domain.Scope, recipeID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.hh(sc)
	if err != nil {
		return err
	}
	if _, ok := h.lib().photos[recipeID]; !ok {
		return fmt.Errorf("photo: %w", domain.ErrNotFound)
	}
	delete(h.lib().photos, recipeID)
	return nil
}
