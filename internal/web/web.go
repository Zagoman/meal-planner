// Package web serves the server-rendered UI (package C): templ pages, htmx
// partials, SSE and the PWA assets. Auth is a session cookie.
//
// # CSRF
//
// Two layers, no per-form tokens:
//
//  1. The session cookie is SameSite=Lax, so browsers do not send it on
//     cross-site POSTs.
//  2. Every unsafe request (POST, PUT, PATCH, DELETE) goes through
//     net/http.CrossOriginProtection: a request whose Sec-Fetch-Site header
//     says cross-site, or whose Origin does not match the Host (or
//     Config.BaseURL), is rejected with 403. All browsers we target send one
//     of the two headers. Requests with neither are not from a browser, so
//     they cannot carry a victim's cookie.
//
// Handlers only change state on POST; GET handlers never write.
package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/httpx"
	"github.com/Zagoman/meal-planner/internal/service"
	assets "github.com/Zagoman/meal-planner/web"
)

// handlers holds only what pages need. Config is read here once and never
// handed to templates.
type handlers struct {
	svc             service.Services
	events          *events.Hub
	secureCookie    bool
	baseURL         string // public origin, for invite links
	generateTimeout time.Duration
	heartbeat       time.Duration
}

func Routes(d app.Deps) http.Handler {
	h := &handlers{
		svc:             d.Services,
		events:          d.Events,
		secureCookie:    strings.HasPrefix(d.Config.BaseURL, "https://"),
		baseURL:         strings.TrimRight(d.Config.BaseURL, "/"),
		generateTimeout: d.Config.GenerateTimeout,
		heartbeat:       25 * time.Second,
	}
	if h.generateTimeout <= 0 {
		h.generateTimeout = 60 * time.Second
	}
	if h.events == nil {
		h.events = events.NewHub()
	}

	csrf := http.NewCrossOriginProtection()
	if h.baseURL != "" {
		if err := csrf.AddTrustedOrigin(h.baseURL); err != nil {
			slog.Warn("BASE_URL is not a valid origin for CSRF checks", "err", err)
		}
	}
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, errCSRF)
	}))

	r := chi.NewRouter()
	r.Use(csrf.Handler, h.session, securityHeaders)

	r.Handle("/static/*", http.StripPrefix("/static/", staticFiles()))
	// The service worker must be served from the root to control every page.
	r.Get("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/")
		http.ServeFileFS(w, r, assets.Static, "sw.js")
	})

	r.Get("/login", h.loginPage)
	r.Post("/login", h.login)
	r.Get("/logout", h.logoutPage)
	r.Post("/logout", h.logout)
	r.Get("/invite/{token}", h.invitePage)
	r.Post("/invite/{token}/signup", h.inviteSignup)

	r.Group(func(r chi.Router) {
		r.Use(h.requireAuth)
		r.Get("/", h.listPage)
		r.Get("/list/stream", h.listStream)
		r.Post("/list/items", h.addItem)
		r.Post("/list/items/{id}/check", h.checkItem)
		r.Post("/list/items/{id}/delete", h.deleteItem)

		r.Get("/week", h.weekPage)
		r.Post("/week/generate", h.generate)
		r.Post("/week/swap", h.swap)
		r.Get("/meals/{id}", h.mealPage)
		r.Post("/meals/{id}/rate", h.rate)

		r.Get("/profile", h.profilePage)
		r.Post("/profile", h.saveProfile)
		r.Post("/profile/sections/move", h.moveSection)
		r.Post("/profile/pantry", h.savePantry)

		r.Get("/household", h.householdPage)
		r.Post("/household/invites", h.createInvite)
		r.Post("/household/switch", h.switchHousehold)
		r.Post("/household/tokens", h.createToken)
		r.Post("/household/password", h.changePassword)
		r.Post("/invite/{token}/accept", h.acceptInvite)
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, domain.ErrNotFound)
	})
	return r
}

func staticFiles() http.Handler {
	fs := http.FileServerFS(assets.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are not fingerprinted; the service worker keeps them for
		// offline use and revalidates in the background.
		w.Header().Set("Cache-Control", "no-cache")
		fs.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin") // invite tokens live in URLs
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

var errCSRF = errors.New("cross-origin request blocked")

// isHX reports whether the request came from htmx (a partial is expected).
// Boosted navigations want whole pages, so they count as full requests.
func isHX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true"
}

func (h *handlers) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodGet && !isHX(r) {
		// Pages carry household data: let the service worker cache them,
		// but never shared caches.
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		slog.ErrorContext(r.Context(), "render", "path", r.URL.Path, "err", err)
	}
}

// redirect sends the browser to url; htmx requests get HX-Redirect so the
// whole page (and its SSE stream) reloads.
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // callers pass constants or safeNext() paths
}

// fail maps a service error to a status (httpx.Status) and renders it: a
// flash message for htmx requests (retargeted to #flash, so the page around
// it, e.g. the current plan, stays), a full error page otherwise.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrUnauthorized) {
		if _, ok := domain.ScopeFrom(r.Context()); !ok || r.URL.Path == "/login" {
			h.toLogin(w, r)
			return
		}
	}
	status, _ := httpx.Status(err)
	if errors.Is(err, errCSRF) {
		status = http.StatusForbidden
	}
	msg, problems := userMessage(err, status)
	if status >= 500 && !errors.Is(err, domain.ErrGeneration) && !errors.Is(err, domain.ErrNotImplemented) {
		slog.ErrorContext(r.Context(), "web request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		w.Header().Set("HX-Retarget", "#flash")
		w.Header().Set("HX-Reswap", "innerHTML")
		h.render(w, r, status, Flash(msg, problems))
		return
	}
	_, signedIn := domain.ScopeFrom(r.Context())
	h.render(w, r, status, ErrorPage(status, msg, problems, signedIn))
}

func userMessage(err error, status int) (string, []string) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return "Please fix the following:", ve.Problems
	case errors.Is(err, errCSRF):
		return "This request came from another site and was blocked.", nil
	case errors.Is(err, domain.ErrGeneration):
		return "Couldn't make a new plan. Your current plan is unchanged. Try again or reword the request.", nil
	case errors.Is(err, domain.ErrNotFound):
		return "Not found.", nil
	case errors.Is(err, domain.ErrForbidden):
		return "Only a household admin can do that.", nil
	case errors.Is(err, domain.ErrConflict):
		// Conflict messages from services are written for people.
		return strings.TrimSuffix(err.Error(), ": "+domain.ErrConflict.Error()), nil
	case errors.Is(err, domain.ErrNotImplemented):
		return "This isn't available yet.", nil
	case errors.Is(err, domain.ErrUnauthorized):
		return "Please log in again.", nil
	case errors.Is(err, context.DeadlineExceeded):
		return "That took too long. Please try again.", nil
	}
	if status >= 500 {
		return "Something went wrong. Please try again.", nil
	}
	return http.StatusText(status), nil
}

func (h *handlers) toLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.RequestURI()
	if r.Method != http.MethodGet || r.Header.Get("HX-Request") == "true" {
		next = ""
		if cur, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
			next = cur.RequestURI()
		}
	}
	to := "/login"
	if safeNext(next) != "/" {
		to += "?next=" + url.QueryEscape(next)
	}
	redirect(w, r, to)
}

// safeNext keeps redirects on this site: a path starting with one slash.
func safeNext(next string) string {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, "/login") || strings.HasPrefix(next, "/logout") {
		return "/"
	}
	return next
}
