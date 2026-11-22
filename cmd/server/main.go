// Command server runs the meal planner: one binary serving the JSON API, the
// web UI and the SSE streams.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/auth"
	"github.com/Zagoman/meal-planner/internal/config"
	"github.com/Zagoman/meal-planner/internal/database"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/household"
	"github.com/Zagoman/meal-planner/internal/server"
	"github.com/Zagoman/meal-planner/internal/service"
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

	// Package A: `server admin password [email]` sets a new random password
	// for the member (default ADMIN_EMAIL) and prints it to stdout, not the log.
	if len(os.Args) > 2 && os.Args[1] == "admin" && os.Args[2] == "password" {
		return adminPassword(ctx, pool, cfg)
	}
	if _, err := household.Bootstrap(ctx, pool, cfg.AdminEmail); err != nil {
		return err
	}

	services := service.NewUnimplemented()
	// Package A.
	authSvc := auth.New(pool, cfg.AdminEmail)
	householdSvc := household.New(pool, cfg.BaseURL, cfg.AdminEmail)
	services.Auth = authSvc
	services.Households = householdSvc
	services.Profiles = householdSvc
	services.Feedback = householdSvc
	// Package B replaces Plans and Lists.

	deps := app.Deps{
		Config:   cfg,
		Pool:     pool,
		Services: services,
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

// adminPassword is package A's `server admin password [email]` command.
func adminPassword(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) error {
	if _, err := household.Bootstrap(ctx, pool, cfg.AdminEmail); err != nil {
		return err
	}
	email := cfg.AdminEmail
	if len(os.Args) > 3 {
		email = os.Args[3]
	}
	if email == "" {
		return errors.New("usage: server admin password <email> (or set ADMIN_EMAIL)")
	}
	pw, err := auth.New(pool, cfg.AdminEmail).ResetPassword(ctx, email)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "New password for %s: %s\n", auth.NormalizeEmail(email), pw)
	return err
}
