package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Zagoman/meal-planner/internal/app"
	"github.com/Zagoman/meal-planner/internal/events"
	"github.com/Zagoman/meal-planner/internal/server"
	"github.com/Zagoman/meal-planner/internal/service"
	"github.com/Zagoman/meal-planner/internal/testutil"
)

type downDB struct{}

func (downDB) Ping(context.Context) error { return errors.New("down") }

func TestHealthz(t *testing.T) {
	t.Run("database up", func(t *testing.T) {
		h := server.New(app.Deps{Pool: testutil.NewDB(t), Services: service.NewUnimplemented(), Events: events.NewHub()})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d", rec.Code)
		}
	})
	t.Run("database down", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.Health(downDB{}).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d", rec.Code)
		}
	})
}

func TestSkeletonRoutes(t *testing.T) {
	h := server.New(app.Deps{Services: service.NewUnimplemented(), Events: events.NewHub()})
	cases := []struct {
		path, wantType string
		wantCode       int
		wantBody       string
	}{
		{"/", "text/html", 200, `hx-boost="true"`},
		{"/static/htmx.min.js", "javascript", 200, "htmx"},
		{"/sw.js", "javascript", 200, "skipWaiting"},
		{"/api/openapi.yaml", "yaml", 200, "openapi: 3.1.0"},
		{"/api/plans/current", "application/json", 501, `"not_implemented"`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.wantCode || !strings.Contains(rec.Header().Get("Content-Type"), c.wantType) ||
			!strings.Contains(rec.Body.String(), c.wantBody) {
			t.Errorf("%s: code=%d type=%q body=%.80q", c.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}
