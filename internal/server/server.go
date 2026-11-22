// Package server assembles the HTTP router: health check, the JSON API under
// /api and the web UI everywhere else.
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/lucaszago/meal-planner/internal/api"
	"github.com/lucaszago/meal-planner/internal/app"
	"github.com/lucaszago/meal-planner/internal/openapi"
	"github.com/lucaszago/meal-planner/internal/web"
)

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

func New(d app.Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)

	var pinger Pinger
	if d.Pool != nil {
		pinger = d.Pool
	}
	r.Get("/healthz", Health(pinger))
	r.Get("/api/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapi.Spec)
	})
	r.Mount("/api", api.Routes(d))
	r.Mount("/", web.Routes(d))
	return r
}

// Health returns 200 when the database answers, 503 otherwise.
func Health(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Error(w, "no database", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}
}
