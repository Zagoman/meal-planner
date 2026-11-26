package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// draftCard is GET /plans/next/card: the Week page's draft card partial.
func (h *handlers) draftCard(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Drafts.Card(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, DraftCard(c))
}

// makeDraft is POST /plans/next/generate: queue a draft, show "Making your
// plan…" at once.
func (h *handlers) makeDraft(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Drafts.RequestDraft(r.Context(), scope(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/week")
		return
	}
	h.draftCard(w, r)
}

// reviewPage is GET /plans/next. Without a draft it goes back to the week.
func (h *handlers) reviewPage(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	rev, err := h.svc.Drafts.Review(r.Context(), s)
	if errors.Is(err, domain.ErrNotFound) {
		// Being made (or it failed): wait here; the card updates over SSE.
		if c, cerr := h.svc.Drafts.Card(r.Context(), s); cerr == nil && (c.State == domain.DraftMaking || c.State == domain.DraftFailed) {
			h.render(w, r, http.StatusOK, WaitingPage(c))
			return
		}
		redirect(w, r, "/week")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	preview, err := h.svc.Drafts.ListPreview(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var rate *domain.RateList
	if l, err := h.svc.Outcomes.RateList(r.Context(), s); err == nil {
		rate = &l
	} else if !errors.Is(err, domain.ErrNotFound) {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, ReviewPage(rev, s.MemberID, preview, rate))
}

// draftMeals is GET /plans/next/meals. When the draft is gone (confirmed on
// another phone), the page goes to the week.
func (h *handlers) draftMeals(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	rev, err := h.svc.Drafts.Review(r.Context(), s)
	if errors.Is(err, domain.ErrNotFound) {
		redirect(w, r, "/week")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, DraftMeals(rev, s.MemberID))
}

// renderMealCard answers with the meal's card, or with nothing when the meal
// is gone, so htmx removes the card.
func (h *handlers) renderMealCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	s := scope(r)
	m, err := h.svc.Drafts.DraftMeal(r.Context(), s, id)
	if errors.Is(err, domain.ErrNotFound) && isHX(r) && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	confirmTime := ""
	if c, err := h.svc.Drafts.Card(r.Context(), s); err == nil {
		confirmTime = c.ConfirmsAt.Format("15:04")
	}
	h.render(w, r, http.StatusOK, DraftMealCard(m, s.MemberID, confirmTime))
}

// vote is POST /meals/{id}/vote with vote=+1, -1 or 0.
func (h *handlers) vote(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("vote")))
	if err != nil {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"vote: +1, -1 or 0"}})
		return
	}
	if _, err := h.svc.Drafts.Vote(r.Context(), scope(r), id, v); err != nil {
		h.fail(w, r, err)
		return
	}
	h.afterMealChange(w, r)
}

// swapDraftMeal is POST /meals/{id}/swap: queue a swap, show "Swapping…".
func (h *handlers) swapDraftMeal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.svc.Drafts.RequestSwap(r.Context(), scope(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	h.afterMealChange(w, r)
}

// removeDraftMeal is DELETE /meals/{id} (POST /meals/{id}/delete without
// JavaScript).
func (h *handlers) removeDraftMeal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.svc.Drafts.RemoveMeal(r.Context(), scope(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/plans/next")
		return
	}
	w.WriteHeader(http.StatusOK) // empty: the card is swapped away
}

func (h *handlers) afterMealChange(w http.ResponseWriter, r *http.Request) {
	if !isHX(r) {
		redirect(w, r, "/plans/next")
		return
	}
	h.renderMealCard(w, r)
}

// ask is POST /plans/next/ask: queue Ask Claude, show the working state.
func (h *handlers) ask(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Drafts.RequestAsk(r.Context(), scope(r), r.PostFormValue("prompt")); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/plans/next")
		return
	}
	h.draftMeals(w, r)
}

// listPreview is GET /plans/next/list-preview: the line on the review page
// for htmx, the whole preview list otherwise.
func (h *handlers) listPreview(w http.ResponseWriter, r *http.Request) {
	l, err := h.svc.Drafts.ListPreview(r.Context(), scope(r))
	if errors.Is(err, domain.ErrNotFound) && !isHX(r) {
		redirect(w, r, "/week")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if isHX(r) {
		h.render(w, r, http.StatusOK, ListPreviewLink(l))
		return
	}
	h.render(w, r, http.StatusOK, ListPreviewPage(l))
}

// confirm is POST /plans/next/confirm. Two phones confirming at once both
// land on the week.
func (h *handlers) confirm(w http.ResponseWriter, r *http.Request) {
	if _, err := h.svc.Drafts.Confirm(r.Context(), scope(r)); err != nil && !errors.Is(err, domain.ErrNotFound) {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/week")
}
