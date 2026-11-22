// Package api serves the JSON API under /api (package A). Contract:
// internal/openapi/openapi.yaml. Auth is a bearer token; every handler reads
// domain.ScopeFrom(ctx) and calls d.Services.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/domain"
	"github.com/Zagoman/meal-planner/internal/httpx"
)

// KeepAlive is how often /api/list/stream sends a comment line.
const KeepAlive = 25 * time.Second

// Routes returns the /api router. It is mounted at /api, so paths here have
// no prefix.
func Routes(d app.Deps) http.Handler { return routes(d, KeepAlive) }

func routes(d app.Deps, keepAlive time.Duration) http.Handler {
	h := &handlers{d: d, keepAlive: keepAlive}
	r := chi.NewRouter()
	r.Use(h.bearer)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, fmt.Errorf("no route %s: %w", r.URL.Path, domain.ErrNotFound))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusMethodNotAllowed, httpx.ErrorBody{Error: "invalid_input", Message: "method not allowed"})
	})

	r.Post("/plans", h.createPlan)
	r.Get("/plans/current", h.currentPlan)
	r.Post("/plans/{plan_id}/meals/{day}/swap", h.swapMeal)
	r.Get("/meals/{meal_id}", h.getMeal)
	r.Post("/meals/{meal_id}/substitute", h.substitute)

	r.Get("/list/active", h.activeList)
	r.Get("/list/stream", h.streamList)
	r.Post("/list/items", h.addItem)
	r.Patch("/list/items/{item_id}", h.updateItem)
	r.Delete("/list/items/{item_id}", h.deleteItem)

	r.Get("/profile", h.getProfile)
	r.Put("/profile", h.putProfile)
	r.Get("/sections", h.getSections)
	r.Put("/sections", h.putSections)
	r.Get("/pantry", h.getPantry)
	r.Put("/pantry", h.putPantry)

	r.Post("/feedback", h.rate)

	r.Post("/households", h.createHousehold)
	r.Post("/households/{household_id}/invites", h.createInvite)
	r.Post("/invites/{token}/accept", h.acceptInvite)
	r.Get("/admin/usage", h.usage)
	return r
}

type handlers struct {
	d         app.Deps
	keepAlive time.Duration
}

// bearer resolves "Authorization: Bearer <token>" to a Scope. Every /api
// route needs one; a missing or bad token is 401.
func (h *handlers) bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
			httpx.WriteError(w, r, fmt.Errorf("missing bearer token: %w", domain.ErrUnauthorized))
			return
		}
		sc, err := h.d.Services.Auth.BearerScope(r.Context(), token)
		if err != nil {
			if errors.Is(err, domain.ErrUnauthorized) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
			}
			httpx.WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(domain.WithScope(r.Context(), sc)))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// scope is always present behind the bearer middleware.
func scope(r *http.Request) domain.Scope {
	sc, _ := domain.ScopeFrom(r.Context())
	return sc
}

// pathID parses a UUID path parameter. A malformed ID cannot name a row of
// the caller's household, so it is 404 like any other unknown ID.
func pathID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s: %w", name, domain.ErrNotFound)
	}
	return id, nil
}
