package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Zagoman/meal-planner/internal/domain"
)

// CookieName is the session cookie shared with package A (internal/auth).
const CookieName = "mp_session"

const sessionMaxAge = 30 * 24 * time.Hour

type tokenKey struct{}

// sessionToken returns the raw session token of the request, if any.
func sessionToken(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}

// session resolves the cookie into a domain.Scope with Auth.SessionScope.
// An unknown or expired session clears the cookie; the request continues
// anonymously and requireAuth decides what to do with it.
func (h *handlers) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		s, err := h.svc.Auth.SessionScope(r.Context(), c.Value)
		if err != nil {
			if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrNotFound) {
				h.clearCookie(w)
			}
			next.ServeHTTP(w, r)
			return
		}
		ctx := domain.WithScope(r.Context(), s)
		ctx = context.WithValue(ctx, tokenKey{}, c.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAuth sends anonymous requests to /login (or 401 for the SSE
// stream, which cannot follow a redirect usefully).
func (h *handlers) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := domain.ScopeFrom(r.Context()); !ok {
			if r.URL.Path == "/list/stream" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h.toLogin(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func scope(r *http.Request) domain.Scope {
	s, _ := domain.ScopeFrom(r.Context())
	return s
}

// Secure follows BASE_URL so plain-http localhost development still works.
func (h *handlers) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is set whenever BASE_URL is https
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *handlers) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is set whenever BASE_URL is https
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}
