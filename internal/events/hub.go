// Package events fans out shopping-list changes to every open SSE stream of a
// household. Channels are keyed by household ID, so a subscriber can only
// ever receive its own household's events.
package events

import (
	"sync"

	"github.com/google/uuid"
)

// Kind of change.
type Kind string

const (
	ItemChanged Kind = "item"    // one list item was added, ticked or edited
	ItemDeleted Kind = "deleted" // one list item was removed
	ListRebuilt Kind = "list"    // the whole list changed (new plan, swap)
)

// Event is what the SSE handlers receive; they render the matching partial.
type Event struct {
	Kind   Kind
	ItemID uuid.UUID // zero for ListRebuilt
}

// Hub is an in-process pub/sub. Slow subscribers drop events rather than
// block publishers; clients recover by refetching on reconnect.
type Hub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[uuid.UUID]map[chan Event]struct{}{}} }

// Subscribe returns a channel of the household's events and a cancel func
// that must be called when the stream ends.
func (h *Hub) Subscribe(householdID uuid.UUID) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	if h.subs[householdID] == nil {
		h.subs[householdID] = map[chan Event]struct{}{}
	}
	h.subs[householdID][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs[householdID], ch)
			if len(h.subs[householdID]) == 0 {
				delete(h.subs, householdID)
			}
			h.mu.Unlock()
			close(ch)
		})
	}
}

// Publish sends an event to every subscriber of the household.
func (h *Hub) Publish(householdID uuid.UUID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[householdID] {
		select {
		case ch <- e:
		default:
		}
	}
}
