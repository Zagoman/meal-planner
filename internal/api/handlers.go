package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/httpx"
)

const maxPrompt = 2000

type promptRequest struct {
	Prompt *string `json:"prompt"`
}

func (p promptRequest) validate() (string, error) {
	if p.Prompt == nil {
		return "", &domain.ValidationError{Problems: []string{"prompt: required"}}
	}
	s := strings.TrimSpace(*p.Prompt)
	if len([]rune(s)) > maxPrompt {
		return "", &domain.ValidationError{Problems: []string{"prompt: at most 2000 characters"}}
	}
	return s, nil
}

// --- plans and meals (service.Plans, package B) ---

func (h *handlers) createPlan(w http.ResponseWriter, r *http.Request) {
	var req promptRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	prompt, err := req.validate()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	plan, err := h.d.Services.Plans.Generate(r.Context(), scope(r), prompt)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, plan)
}

func (h *handlers) currentPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := h.d.Services.Plans.Current(r.Context(), scope(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, plan)
}

func (h *handlers) swapMeal(w http.ResponseWriter, r *http.Request) {
	planID, err := pathID(r, "plan_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	day := domain.Weekday(chi.URLParam(r, "day"))
	if !day.Valid() {
		httpx.WriteError(w, r, &domain.ValidationError{Problems: []string{"day: a lowercase English weekday"}})
		return
	}
	var req promptRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	prompt, err := req.validate()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	meal, err := h.d.Services.Plans.SwapMeal(r.Context(), scope(r), planID, day, prompt)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, meal)
}

func (h *handlers) getMeal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "meal_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	meal, err := h.d.Services.Plans.Meal(r.Context(), scope(r), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, meal)
}

func (h *handlers) substitute(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "meal_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	from, to := strings.TrimSpace(req.From), strings.TrimSpace(req.To)
	var problems []string
	if from == "" || len(from) > 100 {
		problems = append(problems, "from: 1 to 100 characters")
	}
	if to == "" || len(to) > 100 {
		problems = append(problems, "to: 1 to 100 characters")
	}
	if len(problems) > 0 {
		httpx.WriteError(w, r, &domain.ValidationError{Problems: problems})
		return
	}
	meal, err := h.d.Services.Plans.Substitute(r.Context(), scope(r), id, from, to)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, meal)
}

// --- shopping list (service.Lists, package B) ---

func (h *handlers) activeList(w http.ResponseWriter, r *http.Request) {
	l, err := h.d.Services.Lists.Active(r.Context(), scope(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, l)
}

func (h *handlers) addItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Section string `json:"section"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	section := strings.TrimSpace(req.Section)
	if section == "" {
		section = "other"
	}
	if name == "" || len(name) > 100 {
		httpx.WriteError(w, r, &domain.ValidationError{Problems: []string{"name: 1 to 100 characters"}})
		return
	}
	item, err := h.d.Services.Lists.AddItem(r.Context(), scope(r), name, section)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, item)
}

func (h *handlers) updateItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "item_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Checked *bool `json:"checked"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.Checked == nil {
		httpx.WriteError(w, r, &domain.ValidationError{Problems: []string{"checked: required"}})
		return
	}
	item, err := h.d.Services.Lists.SetChecked(r.Context(), scope(r), id, *req.Checked)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, item)
}

func (h *handlers) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "item_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.d.Services.Lists.DeleteItem(r.Context(), scope(r), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- profile, sections, pantry ---

func (h *handlers) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := h.d.Services.Profiles.Get(r.Context(), scope(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *handlers) putProfile(w http.ResponseWriter, r *http.Request) {
	var p domain.Profile
	if err := httpx.DecodeJSON(r, &p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.d.Services.Profiles.Put(r.Context(), scope(r), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) getSections(w http.ResponseWriter, r *http.Request) {
	s, err := h.d.Services.Profiles.Sections(r.Context(), scope(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

func (h *handlers) putSections(w http.ResponseWriter, r *http.Request) {
	var in []domain.Section
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.d.Services.Profiles.PutSections(r.Context(), scope(r), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) getPantry(w http.ResponseWriter, r *http.Request) {
	p, err := h.d.Services.Profiles.Pantry(r.Context(), scope(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *handlers) putPantry(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := httpx.DecodeJSON(r, &names); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.d.Services.Profiles.PutPantry(r.Context(), scope(r), names)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// --- feedback ---

func (h *handlers) rate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MealID uuid.UUID `json:"meal_id"`
		Rating int       `json:"rating"`
		Note   string    `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.MealID == uuid.Nil {
		httpx.WriteError(w, r, &domain.ValidationError{Problems: []string{"meal_id: required"}})
		return
	}
	f, err := h.d.Services.Feedback.Rate(r.Context(), scope(r), req.MealID, req.Rating, req.Note)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, f)
}

// --- households, invites, admin ---

func (h *handlers) createHousehold(w http.ResponseWriter, r *http.Request) {
	sc := scope(r)
	if !sc.ServerAdmin {
		// Checked before the body so non-admins learn nothing from validation.
		httpx.WriteError(w, r, fmt.Errorf("create household: %w", domain.ErrForbidden))
		return
	}
	var req struct {
		Name       string `json:"name"`
		AdminEmail string `json:"admin_email"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hh, err := h.d.Services.Households.Create(r.Context(), sc, req.Name, req.AdminEmail)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, hh)
}

func (h *handlers) createInvite(w http.ResponseWriter, r *http.Request) {
	sc := scope(r)
	id, err := pathID(r, "household_id")
	if err == nil && id != sc.HouseholdID {
		// Another household (even one the caller belongs to under a
		// different token) does not exist for this token.
		err = fmt.Errorf("household: %w", domain.ErrNotFound)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		Role domain.Role `json:"role"`
	}
	if err := decodeOptionalJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	inv, err := h.d.Services.Households.CreateInvite(r.Context(), sc, req.Role)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, inv)
}

func (h *handlers) acceptInvite(w http.ResponseWriter, r *http.Request) {
	m, err := h.d.Services.Households.AcceptInvite(r.Context(), scope(r).MemberID, chi.URLParam(r, "token"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

func (h *handlers) usage(w http.ResponseWriter, r *http.Request) {
	since := time.Now().AddDate(0, 0, -30)
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			httpx.WriteError(w, r, &domain.ValidationError{Problems: []string{"since: a date like 2026-01-31"}})
			return
		}
		since = t
	}
	rows, err := h.d.Services.Households.Usage(r.Context(), scope(r), since)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rows)
}

// decodeOptionalJSON is DecodeJSON for a body that may be absent.
func decodeOptionalJSON(r *http.Request, v any) error {
	br := bufio.NewReader(r.Body)
	if _, err := br.Peek(1); errors.Is(err, io.EOF) {
		return nil
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{br, r.Body}
	return httpx.DecodeJSON(r, v)
}
