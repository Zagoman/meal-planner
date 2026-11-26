# Meal Planner

Self-hosted, multi-household meal planner. The server asks Claude for a week
of meals as JSON (breakfasts, lunches, dinners and baking, counts from the
household profile), builds a shopping list grouped by shop section, and
members tick it off live on their phones.

The build plan is the dev handoff (wireframes on the design canvas), done in
five windows; `HANDOVER.md` says which windows are done and what is next.
**`GAPS.md` lists every known gap and deviation from the handoff: add a row
for anything you find missing, rough or different (even if you fix it later),
and move it to Fixed with the commit when it is done.**

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
4. **Generation never destroys the current plan.** A new plan is saved only
   after validation passes; it becomes `active` (the old one `archived`) in
   one transaction. On failure return an error wrapping
   `domain.ErrGeneration` and leave existing data unchanged. Whole call under
   60 s (`Config.GenerateTimeout`). `active` is what the handoff calls
   "confirmed"; from window 2 a `draft` is a validated plan waiting for the
   household to review it.

## Layout

| Path | What |
|---|---|
| `cmd/server` | wiring: config, DB, migrations, services, HTTP server |
| `migrations/` | goose SQL, embedded, applied on startup |
| `internal/db/` | sqlc output + `queries/*.sql` |
| `internal/domain/` | `Scope`, types, errors, meal JSON schema |
| `internal/service/` | service interfaces + `Unimplemented` |
| `internal/openapi/` | `openapi.yaml`: the `/api` contract |
| `internal/events/` | per-household pub/sub for SSE |
| `internal/httpx/` | JSON + error→status helpers |
| `internal/config`, `internal/database`, `internal/app`, `internal/server`, `internal/testutil` | plumbing |
| `internal/api/`, `internal/auth/`, `internal/household/` | JSON routes, sessions/tokens, invites, Households/Profiles/Feedback services, isolation tests |
| `internal/planner/`, `internal/shopping/`, `internal/claude/anthropic.go` | prompt, Claude client, validation + retry, Plans/Lists services, list builder |
| `internal/web/`, `web/` | templ pages, htmx partials, SSE, service worker, offline tick queue |
| `deploy/` | Dockerfile, compose, backups, `make deploy` |

The packages were first built in parallel with some of them frozen; that is
over. A change now goes in this order: migration, queries, domain and service
interfaces, `make generate`, then services, web and tests. New migrations are
new files (`00003_...sql`), never edits to an applied one.

## Conventions

- Errors: wrap `domain` sentinels (`fmt.Errorf("get plan: %w", domain.ErrNotFound)`);
  `domain.ValidationError{Problems: ...}` for input problems. Map `pgx.ErrNoRows`
  to `domain.ErrNotFound` in services. HTTP code comes from `httpx.Status`.
- IDs are UUIDs (`github.com/google/uuid`). Days are lowercase English
  (`domain.Weekday`); a meal's day is only a suggestion and may be empty.
  Section keys match `^[a-z][a-z0-9_]{0,39}$`.
- Meals have a `meal_type` (breakfast, lunch, dinner, baking, other). The
  profile's `meal_targets` sets how many of each a week holds; a plan must
  match them exactly.
- Every meal Claude plans is linked to a recipe by `meals.recipe_id`
  (`linkRecipe`: a library recipe of the same type and title first), but
  only `recipes.in_library` rows are in the library (`/recipes`,
  `Library.Search`): a meal rated as cooked, saved with "Save to recipes"
  (`Library.SaveMeal`), a recipe the household added, or a tweaked version.
  Others still open by id (past plans, "not cooked last week"); never
  delete them. The planning prompt lists the library's titles to reuse.
  Outcomes (`meal_outcomes`, one per meal) say
  cooked + rating or not cooked; recipes rated 1 and last week's not-cooked
  meals are banned in prompts and validation (`household.banned`).
- The avoid list is the `avoid_items` table (unique per household on
  `normalized_name`), not a profile column.
- Pantry ingredients become list items with `check_home = true` in their real
  section, shown under "Check at home" at the top of the list. `have_it`
  true hides the item from shopping, false (Buy) shows it in its section.
  Progress counts only items to buy. `domain.PantryCheckSection` is only the
  name of that group, never a stored section.
- Quantities: buying units and counted things are whole (`math.Ceil`);
  "need X" only when it differs from what is bought. Water is never listed.
- Tokens (session, API, invite): random 32 bytes, base64url to the client,
  only `sha256` stored (`token_hash`).
- Shopping-list writes publish `events.Event` to `Deps.Events` after commit.
  Ticks survive a rebuild: generated items are upserted by
  `(plan_id, normalized_name)` (`UpsertGeneratedListItem`) and stale ones
  removed with `DeleteStaleGeneratedListItems`; manual items are never touched.
- Every Claude call from the web UI runs as a job (`internal/jobs`, handlers
  in `planner.RegisterJobs`): the request queues it and returns, the result
  arrives as an `events.Event`, a failed job changes no data. Handlers must
  be idempotent (crash retries; scheduled jobs use a dedupe key). Drafts:
  `service.Drafts`; schedules: `planner.Tick` in the household time zone.
- Email goes through `internal/mail` (Resend; log-only without a key; a
  recorder in tests). Never log a sign-in link or put tracking in an email.
  Sign-in links (`login_tokens`) and invites are single use; a join adds the
  membership only when the emailed link is used.
- Server-side fetches of user-given URLs go through `internal/fetch` only
  (public addresses, size and time limits). Uploaded photos go through
  `internal/imagefit`. Import photos are not stored once read; a family's
  recipe photo is kept (resized) in `recipe_photos` and served only to its
  household at `/recipes/{id}/photo`. A recipe without a photo shows the
  line drawing for `domain.DishTypeOf` (derived, never stored).
- Brand: Hvad skal vi ha'? (`web.AppName`). Colours, type and components
  come from `web/static/hvadskalviha.css` (the design system's tokens; use
  `var(--ink)`, `var(--space-4)`, never hex in templates or `app.css`).
  UI text is English; Danish brand lines get `lang="da"`. Never name the AI
  provider in anything a user sees (UI, emails, errors): say "we" or
  describe the action.
- SSE: `GET /events` sends per-partial events (`list.item`, `meal.updated`,
  `meal-<id>`, `plan.updated`, `job.failed`); pages refetch the partial
  with `hx-trigger="sse:<name>"`. Every partial has a stable id. A
  partial with `hx-get`/`hx-select`/`hx-swap` sets `hx-disinherit="*"` so
  the links inside it don't inherit them.
- Claude returns meals only (schema: `internal/domain/meal_plan.schema.json`,
  structs `domain.MealPlanJSON`). The server builds the list. Validate:
  schema, meal count per type equals `meal_targets`, `one_pot` when the
  profile requires it (not for baking), no avoid-list ingredient,
  baby-portion step before salt/stock/cheese (not for baking). Retry once with the problems
  named, then fail. Log usage with `InsertUsage` (kind `plan`, `swap`, `ask`,
  `auto_confirm`, `retry`).
- Tests: `testutil.NewDB(t)` gives each test its own migrated database
  (cloned from a template, fast, safe with `t.Parallel()`).
  `testutil.NewHousehold(t, pool, name)` returns an admin `Scope`.
- `log/slog` JSON logs. No secrets or prompts with personal data at Info.

## Not in the original spec

- `sessions` table (web login), `GET /api/meals/{meal_id}` (recipe screen via API).
- `Plans.Meal` and `Feedback.SaveRecipe/Recipes` service methods.
- `make e2e`: Playwright against `internal/web/e2eserver` (fake services).

## Don't build yet

Supermarket offers, favourites rotation, pantry tracking, baby stages, Home
Assistant, cost estimates.
