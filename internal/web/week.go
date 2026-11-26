package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	card, err := h.svc.Drafts.Card(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, WeekPage(p, ok, card))
}

func (h *handlers) mealPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	s := scope(r)
	m, err := h.svc.Plans.Meal(r.Context(), s, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pic := recipePic{RecipeID: m.RecipeID, Title: m.Title, Dish: domain.DishTypeOf(m), Back: "/meals/" + m.ID.String()}
	if m.RecipeID != nil {
		if pic.PhotoAt, err = h.svc.Library.PhotoAt(r.Context(), s, *m.RecipeID); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	h.render(w, r, http.StatusOK, MealPage(m, pic))
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
