// Command e2eserver runs the real router (server.New) with the in-memory
// webfake services and seed data, for the Playwright tests and for trying
// the UI without Postgres or Claude:
//
//	go run ./internal/web/e2eserver -addr 127.0.0.1:8090
//
// Log in as anna@example.test, ben@example.test or carl@example.test with
// the password webfake.SeedPassword. A prompt containing "fail" makes plan
// generation fail.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
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

	hub := events.NewHub()
	store := webfake.New(hub)
	store.Seed()
	store.GenerateDelay = *delay

	d := app.Deps{
		Config:   config.Config{BaseURL: "http://" + *addr, GenerateTimeout: 55 * time.Second},
		Services: store.Services(),
		Events:   hub,
	}
	slog.Info("e2e server with fake services", "addr", *addr)
	srv := &http.Server{Addr: *addr, Handler: server.New(d), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("stopped", "err", err)
		os.Exit(1)
	}
}
