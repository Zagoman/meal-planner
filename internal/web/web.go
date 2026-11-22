// Package web serves the server-rendered UI (package C): templ pages, htmx
// partials, SSE and the PWA assets. Auth is a session cookie.
package web

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/lucaszago/meal-planner/internal/app"
	assets "github.com/lucaszago/meal-planner/web"
)

func Routes(_ app.Deps) http.Handler {
	r := chi.NewRouter()
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(assets.Static)))
	// The service worker must be served from the root to control every page.
	r.Get("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, assets.Static, "sw.js")
	})
	r.Get("/", templ.Handler(Placeholder("Shopping list", TabList)).ServeHTTP)
	r.Get("/week", templ.Handler(Placeholder("This week", TabWeek)).ServeHTTP)
	r.Get("/profile", templ.Handler(Placeholder("Profile", TabProfile)).ServeHTTP)
	r.Get("/household", templ.Handler(Placeholder("Household", TabHousehold)).ServeHTTP)
	return r
}
