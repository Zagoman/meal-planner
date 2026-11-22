package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

func (h *handlers) currentPlan(r *http.Request) (domain.Plan, bool, error) {
	p, err := h.svc.Plans.Current(r.Context(), scope(r))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Plan{}, false, nil
	}
	return p, err == nil, err
}

func (h *handlers) weekPage(w http.ResponseWriter, r *http.Request) {
	p, ok, err := h.currentPlan(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, WeekPage(p, ok))
}

// generate runs Plans.Generate detached from the request, so locking the
// phone or navigating away does not throw away a plan that is nearly done.
// The service enforces the 60 s budget; the timeout here is a backstop.
func (h *handlers) generate(w http.ResponseWriter, r *http.Request) {
	prompt := strings.TrimSpace(r.PostFormValue("prompt"))
	if len(prompt) > 2000 {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"prompt: at most 2000 characters"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.generateTimeout+5*time.Second)
	defer cancel()
	p, err := h.svc.Plans.Generate(ctx, scope(r), prompt)
	if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, domain.ErrGeneration) {
		err = fmt.Errorf("generate: %w: %w", domain.ErrGeneration, err)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/week")
		return
	}
	h.render(w, r, http.StatusOK, WeekBody(p, true))
}

func (h *handlers) swap(w http.ResponseWriter, r *http.Request) {
	planID, err := uuid.Parse(r.PostFormValue("plan_id"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("swap: plan %w", domain.ErrNotFound))
		return
	}
	day := domain.Weekday(r.PostFormValue("day"))
	if !day.Valid() {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"day: not a weekday"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.generateTimeout+5*time.Second)
	defer cancel()
	m, err := h.svc.Plans.SwapMeal(ctx, scope(r), planID, day, strings.TrimSpace(r.PostFormValue("prompt")))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/week")
		return
	}
	h.render(w, r, http.StatusOK, MealCard(planID, m))
}

func (h *handlers) mealPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m, err := h.svc.Plans.Meal(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, MealPage(m))
}

func (h *handlers) rate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rating, err := strconv.Atoi(r.PostFormValue("rating"))
	if err != nil || rating < 1 || rating > 5 {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"rating: pick 1 to 5"}})
		return
	}
	note := strings.TrimSpace(r.PostFormValue("note"))
	if rs := []rune(note); len(rs) > 500 {
		note = string(rs[:500])
	}
	f, err := h.svc.Feedback.Rate(r.Context(), scope(r), id, rating, note)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/meals/"+id.String())
		return
	}
	h.render(w, r, http.StatusOK, Rated(f))
}
