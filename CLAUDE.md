# Meal Planner

Self-hosted, multi-household meal planner. A household types a request, the
server asks Claude for meals as JSON, builds a shopping list grouped by shop
section, and members tick it off live on their phones.

One Go binary: JSON API under `/api/*` (bearer token), server-rendered web UI
everywhere else (session cookie, templ + htmx + SSE), Postgres.

## Commands

```bash
make tools            # sqlc, templ, goose, golangci-lint (pinned)
make generate         # after editing migrations/, internal/db/queries/ or *.templ
make test             # needs Docker: testcontainers starts postgres:17
make lint
make dev-db && cp .env.example .env && make run   # http://localhost:8080
```

Never edit generated files: `internal/db/*.sql.go`, `models.go`, `querier.go`,
`db.go`, and `*_templ.go`. CI fails if they are stale.

## Hard rules

1. **Household isolation.** Every table except `members` has `household_id`;
   every query filters by it. Services take a `domain.Scope` and use
   `scope.HouseholdID`, never an ID from the request body. Another
   household's row is `domain.ErrNotFound` (404), never 403. Child tables use
   composite `(household_id, id)` foreign keys so the schema rejects
   cross-household rows too. **Every endpoint has a test proving another
   household cannot read or change its data** (use two
   `testutil.NewHousehold` fixtures).
2. **The Anthropic API key never reaches the browser.** It is read only in
   `internal/config` and used only in `internal/claude`. Never pass `Config`
   into templates.
3. **Tests never call the real Claude API.** Use `claude.NewFake(...)`. The
   real client is only constructed in `cmd/server`.
4. **Generation never destroys the current plan.** A new plan is a `draft`;
   it becomes `active` (and the old one `archived`) in one transaction only
   after validation passes. On failure return an error wrapping
   `domain.ErrGeneration`. Whole call under 60 s (`Config.GenerateTimeout`).

## Layout and ownership

| Path | What | Owner |
|---|---|---|
| `cmd/server` | wiring: config, DB, migrations, services, HTTP server | 0 (packages edit only their wiring lines) |
| `migrations/` | goose SQL, embedded, applied on startup | 0, frozen |
| `internal/db/` | sqlc output + `queries/*.sql` | 0, frozen |
| `internal/domain/` | `Scope`, types, errors, meal JSON schema | 0, frozen |
| `internal/service/` | service interfaces + `Unimplemented` | 0, frozen |
| `internal/openapi/` | `openapi.yaml`: the `/api` contract | 0, frozen |
| `internal/events/` | per-household pub/sub for SSE | 0, frozen |
| `internal/httpx/` | JSON + error→status helpers | 0, frozen |
| `internal/config`, `internal/database`, `internal/app`, `internal/server`, `internal/testutil` | plumbing | 0, frozen |
| `internal/api/`, `internal/auth/`, `internal/household/` | JSON routes, sessions/tokens, invites, Households/Profiles/Feedback services, isolation tests | A |
| `internal/planner/`, `internal/shopping/`, `internal/claude/anthropic.go` | prompt, Claude client, validation + retry, Plans/Lists services, list builder | B |
| `internal/web/`, `web/` | templ pages, htmx partials, SSE, service worker, offline tick queue | C |
| `deploy/` | Dockerfile, compose, backups, `make deploy` | D |

**Frozen** means: a package that needs a change there (a new query, a new
interface method, a schema column) stops and asks for it rather than editing
it on its branch. New migrations are new files (`00002_...sql`), never edits
to `00001_init.sql`.

## Conventions

- Errors: wrap `domain` sentinels (`fmt.Errorf("get plan: %w", domain.ErrNotFound)`);
  `domain.ValidationError{Problems: ...}` for input problems. Map `pgx.ErrNoRows`
  to `domain.ErrNotFound` in services. HTTP code comes from `httpx.Status`.
- IDs are UUIDs (`github.com/google/uuid`). Days are lowercase English
  (`domain.Weekday`). Section keys match `^[a-z][a-z0-9_]{0,39}$`;
  `domain.PantryCheckSection` ("pantry_check") is always the last list section.
- Tokens (session, API, invite): random 32 bytes, base64url to the client,
  only `sha256` stored (`token_hash`).
- Shopping-list writes publish `events.Event` to `Deps.Events` after commit.
  Ticks survive a rebuild: generated items are upserted by
  `(plan_id, normalized_name)` (`UpsertGeneratedListItem`) and stale ones
  removed with `DeleteStaleGeneratedListItems`; manual items are never touched.
- Claude returns meals only (schema: `internal/domain/meal_plan.schema.json`,
  structs `domain.MealPlanJSON`). The server builds the list. Validate:
  schema, `one_pot` when the profile requires it, no avoid-list ingredient,
  baby-portion step before salt/stock/cheese. Retry once with the problems
  named, then fail. Log usage with `InsertUsage` (kind `plan`, `swap`, `retry`).
- Tests: `testutil.NewDB(t)` gives each test its own migrated database
  (cloned from a template, fast, safe with `t.Parallel()`).
  `testutil.NewHousehold(t, pool, name)` returns an admin `Scope`.
- `log/slog` JSON logs. No secrets or prompts with personal data at Info.

## Not in the spec but added in package 0

- `sessions` table (web login), `GET /api/meals/{meal_id}` (recipe screen via API).
- `Plans.Meal` and `Feedback.SaveRecipe/Recipes` service methods.

## Don't build yet

Supermarket offers, favourites rotation, pantry tracking, baby stages, Home
Assistant, cost estimates.
