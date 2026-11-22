package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/httpx"
)

// streamList is GET /list/stream. It sends the current list as a `list`
// event, then one event per change of the caller's household:
//
//	event: item     data: ListItem
//	event: deleted  data: {"id": "..."}
//	event: list     data: List
//
// and a ": keep-alive" comment every keepAlive. Data is always loaded
// through service.Lists with the caller's scope, so the hub (keyed by
// household) and the service both enforce isolation.
func (h *handlers) streamList(w http.ResponseWriter, r *http.Request) {
	sc := scope(r)
	ctx := r.Context()
	rc := http.NewResponseController(w)

	// Subscribe before the first load so no change is missed in between.
	ch, cancel := h.d.Events.Subscribe(sc.HouseholdID)
	defer cancel()

	list, err := h.d.Services.Lists.Active(ctx, sc)
	haveList := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		httpx.WriteError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if haveList {
		if err := writeEvent(w, events.ListRebuilt, list); err != nil {
			return
		}
	} else if _, err := fmt.Fprint(w, ": no active list\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	tick := time.NewTicker(h.keepAlive)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_, err = fmt.Fprint(w, ": keep-alive\n\n")
		case e, ok := <-ch:
			if !ok {
				return
			}
			err = h.sendEvent(r, w, sc, e)
		}
		if err == nil {
			err = rc.Flush()
		}
		if err != nil {
			return // client gone
		}
	}
}

func (h *handlers) sendEvent(r *http.Request, w http.ResponseWriter, sc domain.Scope, e events.Event) error {
	switch e.Kind {
	case events.ItemDeleted:
		return writeEvent(w, events.ItemDeleted, struct {
			ID uuid.UUID `json:"id"`
		}{e.ItemID})
	case events.ItemChanged, events.ListRebuilt:
		list, err := h.d.Services.Lists.Active(r.Context(), sc)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			slog.WarnContext(r.Context(), "list stream: load list", "err", err)
			return nil // keep the stream; the next event or reconnect recovers
		}
		if e.Kind == events.ListRebuilt {
			return writeEvent(w, events.ListRebuilt, list)
		}
		for _, sec := range list.Sections {
			for _, it := range sec.Items {
				if it.ID == e.ItemID {
					return writeEvent(w, events.ItemChanged, it)
				}
			}
		}
		return nil // not on the active list (any more)
	}
	return nil
}

func writeEvent(w http.ResponseWriter, kind events.Kind, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
	return err
}
