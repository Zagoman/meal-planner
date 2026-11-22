package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// InviteSignup is implemented by an Auth service that can create an account
// from an invite: it creates the member, accepts the invite and starts a
// session in one step. service.Auth has no such method yet; until it does,
// the invite page only offers "log in, then join".
type InviteSignup interface {
	SignUpWithInvite(ctx context.Context, inviteToken, email, name, password string) (sessionToken string, s domain.Scope, err error)
}

// PasswordChanger is implemented by an Auth service that lets a signed-in
// member change their own password (auth.Service does). The session with
// keepSessionToken stays signed in; the member's other sessions end.
type PasswordChanger interface {
	ChangePassword(ctx context.Context, s domain.Scope, keepSessionToken, current, next string) error
}

func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	pc, ok := h.svc.Auth.(PasswordChanger)
	if !ok {
		h.fail(w, r, domain.ErrNotImplemented)
		return
	}
	next := r.PostFormValue("new_password")
	if next != r.PostFormValue("confirm_password") {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"new password: the two entries do not match"}})
		return
	}
	if err := pc.ChangePassword(r.Context(), scope(r), sessionToken(r.Context()), r.PostFormValue("current_password"), next); err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, PasswordChanged())
}

func (h *handlers) householdPage(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	members, err := h.svc.Households.Members(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ms, err := h.svc.Households.Memberships(r.Context(), s)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_, canChange := h.svc.Auth.(PasswordChanger)
	v := householdView{Scope: s, Members: members, Memberships: ms, Name: "Your household", CanChangePassword: canChange}
	for _, m := range ms {
		if m.HouseholdID == s.HouseholdID {
			v.Name = m.HouseholdName
		}
	}
	h.render(w, r, http.StatusOK, HouseholdPage(v))
}

func (h *handlers) createInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := h.svc.Households.CreateInvite(r.Context(), scope(r), domain.Role(r.PostFormValue("role")))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	link := inv.URL
	if link == "" {
		link = h.baseURL + "/invite/" + url.PathEscape(inv.Token)
	}
	label := fmt.Sprintf("Invite link (%s, expires %s)", inv.Role, inv.ExpiresAt.Format("2 Jan"))
	h.render(w, r, http.StatusOK, ShownOnce(label, link))
}

func (h *handlers) createToken(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		h.fail(w, r, &domain.ValidationError{Problems: []string{"name: required"}})
		return
	}
	tok, err := h.svc.Auth.CreateAPIToken(r.Context(), scope(r), name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, ShownOnce("API token “"+name+"”", tok))
}

func (h *handlers) switchHousehold(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PostFormValue("household_id"))
	if err != nil {
		h.fail(w, r, fmt.Errorf("switch: %w", domain.ErrNotFound))
		return
	}
	if _, err := h.svc.Auth.SwitchHousehold(r.Context(), sessionToken(r.Context()), id); err != nil {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/")
}

func (h *handlers) invitePage(w http.ResponseWriter, r *http.Request) {
	_, signedIn := domain.ScopeFrom(r.Context())
	_, canSignup := h.svc.Auth.(InviteSignup)
	h.render(w, r, http.StatusOK, InvitePage(chi.URLParam(r, "token"), signedIn, canSignup))
}

func (h *handlers) inviteFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		_, signedIn := domain.ScopeFrom(r.Context())
		h.render(w, r, http.StatusNotFound, ErrorPage(http.StatusNotFound,
			"This invite link has expired or was already used. Ask for a new one.", nil, signedIn))
		return
	}
	h.fail(w, r, err)
}

// acceptInvite adds the signed-in member to the invite's household and
// switches the session to it.
func (h *handlers) acceptInvite(w http.ResponseWriter, r *http.Request) {
	s := scope(r)
	m, err := h.svc.Households.AcceptInvite(r.Context(), s.MemberID, chi.URLParam(r, "token"))
	if err != nil {
		h.inviteFailed(w, r, err)
		return
	}
	if _, err := h.svc.Auth.SwitchHousehold(r.Context(), sessionToken(r.Context()), m.HouseholdID); err != nil {
		h.fail(w, r, err)
		return
	}
	redirect(w, r, "/")
}

func (h *handlers) inviteSignup(w http.ResponseWriter, r *http.Request) {
	su, ok := h.svc.Auth.(InviteSignup)
	if !ok {
		h.fail(w, r, domain.ErrNotImplemented)
		return
	}
	tok, _, err := su.SignUpWithInvite(r.Context(), chi.URLParam(r, "token"),
		strings.TrimSpace(r.PostFormValue("email")), strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("password"))
	if err != nil {
		h.inviteFailed(w, r, err)
		return
	}
	h.setCookie(w, tok)
	redirect(w, r, "/")
}

func (h *handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if _, ok := domain.ScopeFrom(r.Context()); ok {
		redirect(w, r, next)
		return
	}
	h.render(w, r, http.StatusOK, LoginPage(next, "", ""))
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PostFormValue("email"))
	next := safeNext(r.PostFormValue("next"))
	tok, _, err := h.svc.Auth.Login(r.Context(), email, r.PostFormValue("password"))
	if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrNotFound) {
		h.render(w, r, http.StatusUnauthorized, LoginPage(next, email, "Wrong email or password."))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setCookie(w, tok)
	redirect(w, r, next)
}

func (h *handlers) logoutPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := domain.ScopeFrom(r.Context()); !ok {
		redirect(w, r, "/login")
		return
	}
	h.render(w, r, http.StatusOK, LogoutPage())
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	if t := sessionToken(r.Context()); t != "" {
		if err := h.svc.Auth.Logout(r.Context(), t); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	h.clearCookie(w)
	redirect(w, r, "/login")
}
