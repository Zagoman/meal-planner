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

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/claude"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/database"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/planner"
	"github.com/Zagoman/meal-planner/internal/server"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/shopping"
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

	hub := events.NewHub()
	services := service.NewUnimplemented()

	// Package B: plans and shopping lists. The real Claude client exists only
	// when a key is configured; without it, plan generation returns 502.
	var claudeClient claude.Client
	if cfg.AnthropicAPIKey != "" {
		claudeClient = claude.NewAnthropic(cfg.AnthropicAPIKey, cfg.ClaudeModel)
	} else {
		slog.Warn("ANTHROPIC_API_KEY is not set: plan generation is disabled")
	}
	services.Lists = shopping.New(pool, hub)
	services.Plans = planner.New(pool, claudeClient, hub, planner.Options{Timeout: cfg.GenerateTimeout})

	deps := app.Deps{
		Config: cfg,
		Pool:   pool,
		// Package A replaces the remaining unimplemented services.
		Services: services,
		Events:   hub,
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
