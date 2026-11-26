package webfake

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
)

// ClearChecked takes the ticked items to buy off the list into a batch.
func (s *Store) ClearChecked(_ context.Context, sc domain.Scope) (uuid.UUID, int, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return uuid.Nil, 0, err
	}
	if h.plan == nil {
		s.mu.Unlock()
		return uuid.Nil, 0, fmt.Errorf("clear: active plan %w", domain.ErrNotFound)
	}
	batch := uuid.New()
	var keep, gone []domain.ListItem
	for _, it := range h.items {
		if it.Checked && it.ToBuy() {
			gone = append(gone, it)
		} else {
			keep = append(keep, it)
		}
	}
	h.items = keep
	if len(gone) > 0 {
		if h.cleared == nil {
			h.cleared = map[uuid.UUID][]domain.ListItem{}
		}
		h.cleared[batch] = gone
	}
	s.mu.Unlock()
	if len(gone) > 0 {
		s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	}
	return batch, len(gone), nil
}

// UndoClear puts one batch back.
func (s *Store) UndoClear(_ context.Context, sc domain.Scope, batch uuid.UUID) (int, error) {
	s.mu.Lock()
	h, err := s.hh(sc)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	items, ok := h.cleared[batch]
	if !ok {
		s.mu.Unlock()
		return 0, fmt.Errorf("undo clear: %w", domain.ErrNotFound)
	}
	delete(h.cleared, batch)
	h.items = append(h.items, items...)
	s.mu.Unlock()
	s.publish(sc.HouseholdID, events.Event{Kind: events.ListRebuilt})
	return len(items), nil
}
