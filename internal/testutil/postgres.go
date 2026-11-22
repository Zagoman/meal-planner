// Package testutil gives tests a real, migrated Postgres and fixtures.
//
// One postgres:17 container is started per test binary. Migrations run once
// into a template database; every NewDB call clones it, so each test gets an
// empty, fully migrated database in a few milliseconds and tests can run in
// parallel.
package testutil

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Zagoman/meal-planner/internal/database"
)

const templateDB = "mp_template"

var (
	once     sync.Once
	adminURL string // connection to the "postgres" maintenance database
	setupErr error
)

func start() {
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		setupErr = fmt.Errorf("start postgres container (is Docker running?): %w", err)
		return
	}
	adminURL, err = c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		setupErr = err
		return
	}
	if err := exec(ctx, adminURL, "CREATE DATABASE "+templateDB); err != nil {
		setupErr = err
		return
	}
	pool, err := database.Open(ctx, withDB(adminURL, templateDB), 30*time.Second)
	if err != nil {
		setupErr = err
		return
	}
	defer pool.Close()
	setupErr = database.Migrate(ctx, pool)
}

// NewDB returns a pool to a fresh, migrated database that is dropped when
// the test ends.
func NewDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(start)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	ctx := context.Background()
	name := "t_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := exec(ctx, adminURL, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, withDB(adminURL, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = exec(context.Background(), adminURL, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})
	return pool
}

func exec(ctx context.Context, url, sql string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

func withDB(url, db string) string {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		panic(err)
	}
	return strings.Replace(url, "/"+cfg.Database+"?", "/"+db+"?", 1)
}
