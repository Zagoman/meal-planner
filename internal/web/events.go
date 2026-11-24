package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/events"
)

// sseNames are the SSE event names one hub event is sent as. Each names the
// partial to refresh; pages listen with hx-trigger="sse:<name>" and fetch
// that partial again. A meal also gets its own name, "meal-<id>", so only
// its card refetches.
func sseNames(e events.Event) []string {
	switch e.Kind {
	case events.ItemChanged, events.ItemDeleted, events.ListRebuilt:
		return []string{"list.item"}
	case events.MealUpdated:
		return []string{"meal.updated", "meal-" + e.MealID.String()}
	case events.PlanUpdated:
		return []string{"plan.updated"}
	case events.JobFailed:
		names := []string{"job.failed"}
		if e.MealID != uuid.Nil {
			names = append(names, "meal-"+e.MealID.String())
		}
		return names
	}
	return nil
}

// eventStream is GET /events: one stream per household (the hub is keyed by
// Scope.HouseholdID, so events never cross households). Data is the item or
// meal id, or empty.
func (h *handlers) eventStream(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	rc := http.NewResponseController(w)
	ch, cancel := h.events.Subscribe(s.HouseholdID)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// A first comment so proxies flush the headers at once.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}
	tick := time.NewTicker(h.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			data := ""
			if e.ItemID != uuid.Nil {
				data = e.ItemID.String()
			} else if e.MealID != uuid.Nil {
				data = e.MealID.String()
			}
			for _, name := range sseNames(e) {
				if err := writeSSE(w, name, data); err != nil {
					return
				}
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
