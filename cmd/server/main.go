// Command server runs the meal planner: one binary serving the JSON API, the
// web UI and the SSE streams.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lucaszago/meal-planner/internal/app"
	"github.com/lucaszago/meal-planner/internal/config"
	"github.com/lucaszago/meal-planner/internal/database"
	"github.com/lucaszago/meal-planner/internal/events"
	"github.com/lucaszago/meal-planner/internal/server"
	"github.com/lucaszago/meal-planner/internal/service"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL, 60*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return err
	}

	deps := app.Deps{
		Config: cfg,
		Pool:   pool,
		// Packages A and B replace these with real implementations.
		Services: service.NewUnimplemented(),
		Events:   events.NewHub(),
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(deps),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE streams stay open and plan generation takes up to 60 s.
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
