// Package app holds the dependencies wired in cmd/server and handed to the
// API and web routers.
package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lucaszago/meal-planner/internal/config"
	"github.com/lucaszago/meal-planner/internal/events"
	"github.com/lucaszago/meal-planner/internal/service"
)

type Deps struct {
	Config   config.Config
	Pool     *pgxpool.Pool
	Services service.Services
	Events   *events.Hub
}
