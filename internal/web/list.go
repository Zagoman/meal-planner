package web

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func (h *handlers) listPage(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	l, err := h.svc.Lists.Active(r.Context(), s)
	hasPlan := true
	if errors.Is(err, domain.ErrNotFound) {
		hasPlan, err = false, nil
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var sections []domain.Section
	if hasPlan {
		if sections, err = h.svc.Profiles.Sections(r.Context(), s); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	h.render(w, r, http.StatusOK, ListPage(l, hasPlan, sections))
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, fmt.Errorf("bad id: %w", domain.ErrNotFound)
	}
	return id, nil
}

// checkItem sets an item's ticked state to the posted value (not a toggle,
// so a tick replayed from the offline queue is idempotent) and returns the
// item partial.
func (h *handlers) checkItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	checked, err := strconv.ParseBool(r.PostFormValue("checked"))
	if err != nil {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"checked: must be true or false"}})
		return
	}
	it, err := h.svc.Lists.SetChecked(r.Context(), scope(r), id, checked)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/")
		return
	}
	h.render(w, r, http.StatusOK, Item(it))
}

func (h *handlers) addItem(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	if _, err := h.svc.Lists.AddItem(r.Context(), s, strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("section")); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/")
		return
	}
	h.renderListBody(w, r)
}

func (h *handlers) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.Lists.DeleteItem(r.Context(), scope(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/")
		return
	}
	w.WriteHeader(http.StatusOK) // empty body: the row is swapped away
}

func (h *handlers) renderListBody(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.Lists.Active(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, ListBody(l))
}

// listStream is the page-side SSE endpoint. It subscribes to the caller's
// household only (the hub is keyed by Scope.HouseholdID) and, on connect and
// after every change, sends the whole list as an htmx partial in a "list"
// event, which the page swaps into #list. Sending the full list on connect
// also catches a reconnecting phone up on anything it missed.
func (h *handlers) listStream(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	rc := http.NewResponseController(w)
	ch, cancel := h.events.Subscribe(s.HouseholdID)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no") // nginx/caddy: do not buffer
	w.WriteHeader(http.StatusOK)

	send := func() error {
		l, err := h.svc.Lists.Active(r.Context(), s)
		var buf bytes.Buffer
		switch {
		case errors.Is(err, domain.ErrNotFound):
			buf.WriteString(`<p class="muted">The plan changed. <a href="/">Reload</a></p>`)
		case err != nil:
			return err
		default:
			if err := ListBody(l).Render(r.Context(), &buf); err != nil {
				return err
			}
		}
		if err := writeSSE(w, "list", buf.String()); err != nil {
			return err
		}
		return rc.Flush()
	}
	if err := send(); err != nil {
		return
	}
	tick := time.NewTicker(h.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			// Coalesce a burst (a rebuild publishes many) into one render.
			drain(ch)
			if err := send(); err != nil {
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

func drain[T any](ch <-chan T) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// writeSSE writes one event; every line of data gets its own "data:" field.
func writeSSE(w http.ResponseWriter, event, data string) error {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteByte('\n')
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := fmt.Fprint(w, b.String())
	return err
}
