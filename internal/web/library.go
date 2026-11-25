package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// ratePage is GET /plans/current/rate: the plan that is ending, meal by meal.
func (h *handlers) ratePage(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	l, err := h.svc.Outcomes.RateList(r.Context(), s)
	if errors.Is(err, domain.ErrNotFound) {
		redirect(w, r, "/plans/next")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := h.svc.Drafts.Card(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, RatePage(l, c.State == domain.DraftReady))
}

// setOutcome is POST /meals/{id}/outcome with rating=1..5, not_cooked=1,
// clear=1 and/or note. It answers with the meal's card and the progress.
func (h *handlers) setOutcome(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	in := domain.OutcomeInput{
		NotCooked: r.PostFormValue("not_cooked") == "1",
		Clear:     r.PostFormValue("clear") == "1",
	}
	if v := strings.TrimSpace(r.PostFormValue("rating")); v != "" {
		if in.Rating, err = strconv.Atoi(v); err != nil || in.Rating == 0 {
			h.fail(w, r, &domain.ValidationError{Problems: []string{"rating: 1 to 5"}})
			return
		}
	}
	if _, ok := r.PostForm["note"]; ok {
		n := r.PostFormValue("note")
		in.Note = &n
	}
	s := scope(r)
	m, err := h.svc.Outcomes.SetOutcome(r.Context(), s, id, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/plans/current/rate")
		return
	}
	l, err := h.svc.Outcomes.RateList(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, outcomeSwap(m, l))
}

// notCooked is GET /plans/next/not-cooked: the review page's section.
func (h *handlers) notCooked(w http.ResponseWriter, r *http.Request) {
	rev, err := h.svc.Drafts.Review(r.Context(), scope(r))
	if errors.Is(err, domain.ErrNotFound) {
		h.render(w, r, http.StatusOK, NotCookedSection(nil, false))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, NotCookedSection(rev.NotCooked, false))
}

// addDraftMeal is POST /plans/next/meals with recipe_id and from
// (not_cooked or library): the meal moves into #draft-meals.
func (h *handlers) addDraftMeal(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PostFormValue("recipe_id"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("recipe: %w", domain.ErrNotFound))
		return
	}
	from := domain.MealSource(r.PostFormValue("from"))
	if from == "" {
		from = domain.FromLibrary
	}
	s := scope(r)
	if _, err := h.svc.Library.AddToNextWeek(r.Context(), s, id, from); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/plans/next")
		return
	}
	rev, err := h.svc.Drafts.Review(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, addBackSwap(rev, s.MemberID))
}

// recipesPage is GET /recipes?q=&filter=; htmx gets only the results.
func (h *handlers) recipesPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := domain.RecipeFilter(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = domain.FilterAll
	}
	list, err := h.svc.Library.Search(r.Context(), scope(r), q, filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if isHX(r) {
		h.render(w, r, http.StatusOK, RecipeResults(list))
		return
	}
	h.render(w, r, http.StatusOK, RecipesPage(q, filter, list))
}

func recipeID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, fmt.Errorf("bad recipe id: %w", domain.ErrNotFound)
	}
	return id, nil
}

// recipePage is GET /recipes/{id}.
func (h *handlers) recipePage(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.Library.RecipeDetail(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, RecipeDetailPage(d))
}

// tweakBox is GET /recipes/{id}/tweak: the partial the SSE event refetches.
func (h *handlers) tweakBox(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.svc.Library.RecipeDetail(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, TweakBox(d))
}

// tweak is POST /recipes/{id}/tweak: queue the job, show "tweaking".
func (h *handlers) tweak(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.svc.Library.RequestTweak(r.Context(), scope(r), id, r.PostFormValue("prompt")); err != nil {
		h.fail(w, r, err)
		return
	}
	if !isHX(r) {
		redirect(w, r, "/recipes/"+id.String())
		return
	}
	h.tweakBox(w, r)
}

// addNext is POST /recipes/{id}/add-next: a toast says where it went.
func (h *handlers) addNext(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.svc.Library.AddToNextWeek(r.Context(), scope(r), id, domain.FromLibrary); err != nil {
		h.fail(w, r, err)
		return
	}
	h.toast(w, r, "/recipes/"+id.String(), "Added to next week's draft.")
}

// addThis is POST /recipes/{id}/add-this.
func (h *handlers) addThis(w http.ResponseWriter, r *http.Request) {
	id, err := recipeID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	n, err := h.svc.Library.AddToThisWeek(r.Context(), scope(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	msg := "Everything is already on this week's list."
	switch n {
	case 0:
	case 1:
		msg = "Added 1 ingredient to this week's list."
	default:
		msg = fmt.Sprintf("Added %d ingredients to this week's list.", n)
	}
	h.toast(w, r, "/recipes/"+id.String(), msg)
}

// toast shows a short message in #flash for htmx, or goes back to back.
func (h *handlers) toast(w http.ResponseWriter, r *http.Request, back, msg string) {
	if !isHX(r) {
		redirect(w, r, back)
		return
	}
	w.Header().Set("HX-Retarget", "#flash")
	w.Header().Set("HX-Reswap", "innerHTML")
	h.render(w, r, http.StatusOK, Notice(msg))
}
