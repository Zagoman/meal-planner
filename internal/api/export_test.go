package api

import (
	"net/http"
	"time"

	"github.com/Zagoman/meal-planner/internal/app"
)

// RoutesWithKeepAlive lets tests use a short SSE keep-alive.
func RoutesWithKeepAlive(d app.Deps, keepAlive time.Duration) http.Handler {
	return routes(d, keepAlive)
}
