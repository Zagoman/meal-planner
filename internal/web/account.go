package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// requestLink is POST /auth/request: always "check your inbox".
func (h *handlers) requestLink(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PostFormValue("email"))
	if err := h.svc.Auth.RequestLoginLink(r.Context(), email); err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			h.render(w, r, http.StatusUnprocessableEntity, LoginPage("", email, "That doesn't look like an email address."))
			return
		}
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, CheckInboxPage(email))
}

// authPage is GET /auth/{token}: a button that signs in. The link is only
// used by the POST, so mail scanners that open links don't use it up.
func (h *handlers) authPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, AuthConfirmPage(chi.URLParam(r, "token")))
}

// authSignIn is POST /auth/{token}: uses the link and starts a session.
func (h *handlers) authSignIn(w http.ResponseWriter, r *http.Request) {
	tok, _, err := h.svc.Auth.SignInWithLink(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) {
			h.render(w, r, http.StatusNotFound, LoginPage("", "", "This sign-in link was already used or has expired. Ask for a new one below."))
			return
		}
		h.fail(w, r, err)
		return
	}
	h.setCookie(w, tok)
	redirect(w, r, "/week") // the setup check sends a new household to /setup
}

// joinPage is GET /join/{token} (H1).
func (h *handlers) joinPage(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	p, err := h.svc.Auth.InvitePreview(r.Context(), token)
	if err != nil {
		h.inviteFailed(w, r, err)
		return
	}
	_, signedIn := domain.ScopeFrom(r.Context())
	h.render(w, r, http.StatusOK, JoinPage(p, token, signedIn, "", ""))
}

// join is POST /join/{token} with name and email: emails a sign-in link.
func (h *handlers) join(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	name, email := strings.TrimSpace(r.PostFormValue("name")), strings.TrimSpace(r.PostFormValue("email"))
	if err := h.svc.Auth.Join(r.Context(), token, name, email); err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			p, perr := h.svc.Auth.InvitePreview(r.Context(), token)
			if perr == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				h.render(w, r, http.StatusUnprocessableEntity, JoinPage(p, token, false, name, email))
				return
			}
		}
		h.inviteFailed(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, CheckInboxPage(email))
}

// oldInvite sends the old invite links (/invite/{token}) to /join.
func (h *handlers) oldInvite(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/join/"+url.PathEscape(chi.URLParam(r, "token")), http.StatusMovedPermanently)
}

// setupPage is GET /setup (H2).
func (h *handlers) setupPage(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Profiles.Get(r.Context(), scope(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, SetupPage(p))
}

// setup is POST /setup: saves the answers, queues the first draft and goes
// to the review, which shows "Making your plan…" until it is ready.
func (h *handlers) setup(w http.ResponseWriter, r *http.Request) {
	var problems []string
	num := func(field string) int {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(field)))
		if err != nil {
			problems = append(problems, field+": a whole number")
		}
		return n
	}
	in := domain.SetupInput{
		MealTargets:  map[domain.MealType]int{},
		People:       num("people"),
		BabyPortions: r.PostFormValue("baby_portions") == "on",
		ShoppingDay:  domain.Weekday(r.PostFormValue("shopping_day")),
		Cuisines:     r.PostForm["cuisine"],
	}
	for _, t := range domain.PlannedMealTypes {
		in.MealTargets[t] = num("meals_" + string(t))
	}
	for _, a := range strings.Split(r.PostFormValue("avoid"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			in.Avoid = append(in.Avoid, a)
		}
	}
	if len(problems) > 0 {
		h.fail(w, r, &domain.ValidationError{Problems: problems})
		return
	}
	s := scope(r)
	if _, err := h.svc.Profiles.Setup(r.Context(), s, in); err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.svc.Drafts.RequestDraft(r.Context(), s); err != nil {
		// Set up, but no plan yet (no Claude key, say): the Week page's card
		// shows how to try again.
		h.fail(w, r, fmt.Errorf("you're set up, but the first plan couldn't be started: %w", err))
		return
	}
	redirect(w, r, "/plans/next")
}

// requireSetup sends members of a household that has not answered the setup
// questions to /setup when they open a main page.
func (h *handlers) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && !isHX(r) {
			if p, err := h.svc.Profiles.Get(r.Context(), scope(r)); err == nil && !p.SetupDone {
				redirect(w, r, "/setup")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// switchTo is POST /households/{id}/switch.
func (h *handlers) switchTo(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("switch: %w", domain.ErrNotFound))
		return
	}
	if _, err := h.svc.Auth.SwitchHousehold(r.Context(), sessionToken(r.Context()), id); err != nil {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/week")
}

// householdView builds the household part of Profile.
func (h *handlers) householdView(r *http.Request) (householdView, error) {
	s := scope(r)
	members, err := h.svc.Households.Members(r.Context(), s)
	if err != nil {
		return householdView{}, err
	}
	ms, err := h.svc.Households.Memberships(r.Context(), s)
	if err != nil {
		return householdView{}, err
	}
	_, canChange := h.svc.Auth.(PasswordChanger)
	v := householdView{Scope: s, Members: members, Memberships: ms, Name: "Your household", CanChangePassword: canChange}
	for _, m := range ms {
		if m.HouseholdID == s.HouseholdID {
			v.Name = m.HouseholdName
		}
	}
	return v, nil
}
