package web

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
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
	var it domain.ListItem
	if at := r.PostFormValue("at"); at != "" {
		// An offline tick replayed later: applied only if it is the latest.
		t, perr := time.Parse(time.RFC3339Nano, at)
		if perr != nil {
			h.fail(w, r, &domain.ValidationError{Problems: []string{"at: an RFC 3339 time"}})
			return
		}
		it, err = h.svc.Lists.TickAt(r.Context(), scope(r), id, checked, t)
	} else {
		it, err = h.svc.Lists.SetChecked(r.Context(), scope(r), id, checked)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/list")
		return
	}
	// The item's section comes back re-sorted (ticked items sink, #1) with
	// the progress out of band. An item no longer in a section (cleared, or
	// a Check at home item) answers with the whole list instead.
	l, err := h.svc.Lists.Active(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for _, sec := range l.Sections {
		for _, x := range sec.Items {
			if x.ID == it.ID {
				h.render(w, r, http.StatusOK, tickSwap(sec, l))
				return
			}
		}
	}
	w.Header().Set("HX-Retarget", "#list")
	w.Header().Set("HX-Reswap", "innerHTML")
	h.renderListBody(w, r)
}

// clearTicked is POST /list/clear (#6): the ticked items to buy leave the
// list (soft delete); the answer is the list plus an Undo toast.
func (h *handlers) clearTicked(w http.ResponseWriter, r *http.Request) {
	batch, n, err := h.svc.Lists.ClearChecked(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/list")
		return
	}
	body, ok := h.listBody(w, r)
	if !ok {
		return
	}
	if n == 0 {
		h.render(w, r, http.StatusOK, body)
		return
	}
	h.render(w, r, http.StatusOK, templ.Join(body, ClearedToast(n, batch.String())))
}

// undoClear is POST /list/clear/undo with batch: those items come back.
func (h *handlers) undoClear(w http.ResponseWriter, r *http.Request) {
	batch, err := uuid.Parse(r.PostFormValue("batch"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("batch: %w", domain.ErrNotFound))
		return
	}
	if _, err := h.svc.Lists.UndoClear(r.Context(), scope(r), batch); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/list")
		return
	}
	body, ok := h.listBody(w, r)
	if !ok {
		return
	}
	h.render(w, r, http.StatusOK, templ.Join(body, EmptyFlash()))
}

// haveItem answers a Check at home item: have_it=true (Have it), false
// (Buy) or empty (clear). It answers with the whole list, since a Buy item
// also moves into its section.
func (h *handlers) haveItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var haveIt *bool
	if v := r.PostFormValue("have_it"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			h.fail(w, r, &domain.ValidationError{Problems: []string{"have_it: must be true, false or empty"}})
			return
		}
		haveIt = &b
	}
	if _, err := h.svc.Lists.SetHaveIt(r.Context(), scope(r), id, haveIt); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/list")
		return
	}
	h.renderListBody(w, r)
}

func (h *handlers) addItem(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	if _, err := h.svc.Lists.AddItem(r.Context(), s, strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("section")); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/list")
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
		redirect(w, r, "/list")
		return
	}
	w.WriteHeader(http.StatusOK) // empty body: the row is swapped away
}

func (h *handlers) renderListBody(w http.ResponseWriter, r *http.Request) {
	if body, ok := h.listBody(w, r); ok {
		h.render(w, r, http.StatusOK, body)
	}
}

// listBody loads the active list and the sections (for the add form).
func (h *handlers) listBody(w http.ResponseWriter, r *http.Request) (templ.Component, bool) {
	s := scope(r)
	l, err := h.svc.Lists.Active(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return nil, false
	}
	secs, err := h.svc.Profiles.Sections(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return nil, false
	}
	return ListBody(l, secs), true
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
			buf.WriteString(`<p class="muted">The plan changed. <a href="/list">Reload</a></p>`)
		case err != nil:
			return err
		default:
			secs, err := h.svc.Profiles.Sections(r.Context(), s)
			if err != nil {
				return err
			}
			if err := ListBody(l, secs).Render(r.Context(), &buf); err != nil {
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
		case e, ok := <-ch:
			if !ok {
				return
			}
			// Coalesce a burst (a rebuild publishes many) into one render,
			// and skip bursts with no list change (votes on a draft).
			if !drainList(ch, e.IsList()) {
				continue
			}
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

// drainList empties ch and reports whether it or any drained event changed
// the list.
func drainList(ch <-chan events.Event, list bool) bool {
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return list
			}
			list = list || e.IsList()
		default:
			return list
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
