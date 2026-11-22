// Package api serves the JSON API under /api (package A). Contract:
// internal/openapi/openapi.yaml. Auth is a bearer token; every handler reads
// domain.ScopeFrom(ctx) and calls d.Services.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lucaszago/meal-planner/internal/app"
	"github.com/lucaszago/meal-planner/internal/domain"
	"github.com/lucaszago/meal-planner/internal/httpx"
)

// Routes is a placeholder until package A lands: every route answers 501.
func Routes(_ app.Deps) http.Handler {
	r := chi.NewRouter()
	r.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, domain.ErrNotImplemented)
	})
	return r
}
