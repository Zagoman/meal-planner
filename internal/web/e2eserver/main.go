// Command e2eserver runs the real router (server.New) with the in-memory
// webfake services and seed data, for the Playwright tests and for trying
// the UI without Postgres or Claude:
//
//	go run ./internal/web/e2eserver -addr 127.0.0.1:8090
//
// Log in as anna@example.test, ben@example.test or carl@example.test with
// the password webfake.SeedPassword. A prompt containing "fail" makes plan
// generation fail. POST /__e2e/reset restores the seed data (this binary
// only; the real server has no such route).
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/server"
	"github.com/Zagoman/meal-planner/internal/web/webfake"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	delay := flag.Duration("generate-delay", 2*time.Second, "simulated plan generation time")
	flag.Parse()

	fresh := func() http.Handler {
		hub := events.NewHub()
		store := webfake.New(hub)
		store.Seed()
		store.GenerateDelay = *delay
		return server.New(app.Deps{
			Config:   config.Config{BaseURL: "http://" + *addr, GenerateTimeout: 55 * time.Second},
			Services: store.Services(),
			Events:   hub,
		})
	}
	var current atomic.Pointer[http.Handler]
	h := fresh()
	current.Store(&h)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /__e2e/reset", func(w http.ResponseWriter, _ *http.Request) {
		h := fresh()
		current.Store(&h)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		(*current.Load()).ServeHTTP(w, r)
	})

	slog.Info("e2e server with fake services", "addr", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("stopped", "err", err)
		os.Exit(1)
	}
}
