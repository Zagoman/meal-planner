# Handover

Where the build stands against the dev handoff (five windows). Update this at
the end of every window.

## Window 1 – Catch up the basics (branch `window-1-basics`)

**Built**

- Migration `00002_meal_types_avoid_check_home.sql`:
  - `profiles`: `meal_targets` jsonb (breakfast, lunch, dinner, baking),
    `people` (was `servings`), `auto_confirm_time` (08:00). `dinners_per_week`
    becomes `meal_targets.dinner`; the `avoid` array moves to `avoid_items`.
  - `avoid_items` table, unique per household on `normalized_name`, with
    `source` (setup, recipe, profile) and `added_by`.
  - `meals`: `meal_type`, optional `day` (suggested), `position`, `recipe_id`,
    `added_from`; a day can now hold several meals.
  - `list_items`: `check_home`, `have_it`, `added_by`. Old `pantry_check`
    rows become Check at home items in their real section.
  - Down migration included; `internal/database/migrate_00002_test.go` checks
    the data copy.
- Planner: the prompt asks for exactly the profile's meal counts per type;
  validation rejects a wrong count per type and any avoid-list ingredient
  (avoid list read from `avoid_items`), then retries once. Water is never
  saved as an ingredient. Default max tokens raised to 32 000 for a full week.
- List builder: pantry items are flagged `check_home` and keep their section;
  buying units and counted things are rounded up to whole units; "need X" only
  when it differs from what is bought; water is never listed.
- Lists service: `SetHaveIt` (Have it / Buy / clear). Progress counts only
  items to buy.
- Profiles service: meal targets, people, auto-confirm time; `AvoidItems`,
  `AddAvoid`, `DeleteAvoid`; `Put` with `avoid` replaces the list, with
  `avoid: nil` leaves it alone.
- Web (matches wireframes S1 List and P1 Week without the draft card):
  - `GET /list` (and `/`): progress bar, Check at home card with Have it / Buy
    (`POST /list/items/{id}/home`), sections, one-row add bar.
    `POST /list/items/{id}/toggle` is the tick route (`/check` still works).
  - `GET /week`: flat list of meals, "Dinner · 30 min · suggested Wed",
    "Make a new plan" folded under the list.
  - `GET /profile`: meals per week per type, people, plan confirm time,
    avoid list as chips (`POST /profile/avoid`, `DELETE /profile/avoid/{id}`),
    link to household settings.
  - Bottom tabs: List · Week · Recipes · Profile. `/recipes` is a plain list
    of saved recipes until window 3.
  - Design tokens from the handoff in `web/static/app.css` (light theme).
- API: profile JSON now has `meal_targets`, `people`, `auto_confirm_time`;
  meals have `meal_type`, `added_from`, optional `day`; lists have
  `check_home`. `openapi.yaml` updated.
- Tests: `make test`, `make lint` and `make e2e` pass locally. The e2e flow
  now also answers Buy / Have it on one phone and checks the other phone.

**Deployed** 2026-11-24 as `6747c74ab3c1` (`make deploy`, healthy in the
container and through Caddy). A dump was taken first:
`mealplanner-20261124-184308.dump`. Migration 2 applied on start; the profile
kept its 4 dinners (`meal_targets.dinner`), people 2, shopping day Sunday.
The avoid list and pantry were empty, so no avoid or Check at home items
yet. 1 active and 3 archived plans and 104 list items are unchanged. The
signed-in pages have not been tried on a phone yet: that needs the account's
own sign-in.

**Not done in this window**

- The handoff's "attach the two existing accounts to one household as
  admins" migration is not needed: production has one account, already the
  admin of its household.

**Known issues**

- htmx swaps: adding an item and Have it / Buy swap the whole `#list`, and
  the SSE stream still sends the whole list as one `list` event. The stable
  ids (`#progress`, `#check-home`, `#section-{key}`, `#item-{id}`) are in
  place for window 2's per-partial events on `GET /events`.
- (Fixed in window 2: swaps are by meal id in the review, and the web UI's
  Claude calls run as jobs.)
- The migration's SQL normalization of old avoid entries does not make the
  last word singular ("mushrooms" stays "mushrooms"); matching still works
  because validation compares whole words, and the app normalizes new
  entries.
- Fractional counts still show "need 0.5" next to a whole buying unit (for
  example "1 net · need 0.5").
- No JSON API endpoints yet for Have it / Buy or for adding or removing one
  avoid entry (the profile `PUT` replaces the whole list).

## Window 2 – Draft and confirm (branch `window-2-draft-confirm`)

Open questions answered with the handoff's defaults: any thumbs-down is
swapped at auto-confirm; the weekly draft is made at 18:00 (not a setting).

**Built**

- Migration `00003_drafts_votes_jobs.sql`:
  - `plans`: `confirmed_by`, `confirmed_at`, `auto_confirmed`, `confirm_note`;
    at most one draft per household and week (`plans_one_draft_per_week`).
    `active` stays the stored name for "confirmed".
  - `profiles.time_zone` (default Europe/Copenhagen; not editable yet).
  - `meal_votes` (+1 / −1, one per member and meal).
  - `jobs` (kind, status, dedupe key, payload, result, error, attempts).
  - `usage_log.kind` allows `ask`, `auto_confirm`, `tweak`, `import`.
- `internal/jobs`: queue + worker in the binary. Claims with `FOR UPDATE SKIP
  LOCKED` (2 goroutines), 90 s per job, a job left running by a crash is
  retried up to three times. Scheduled jobs carry a dedupe key so they fire
  once.
- `service.Drafts` (implemented in `internal/planner/drafts.go`):
  draft card state, review with votes, vote, swap (in place: same meal id,
  votes cleared), remove, Ask Claude (Claude returns the whole set; meals it
  keeps by type + title keep their id and votes), list preview (never
  stored), confirm.
  - Confirm locks the draft row: two phones confirming at once confirm once.
    A draft for the week already under way (or the same week as the active
    plan) keeps manual items, ticks and Have it answers of items still
    needed; a draft for next week starts a fresh list.
  - A draft targets the next shopping day; if that week already has a
    confirmed plan, the week after.
  - Schedules (`Tick`, every minute, household time zone): weekly draft from
    18:00 the evening before the shopping day (skipped if that week already
    has a draft or plan); auto-confirm on the shopping day from the
    auto-confirm time. Auto-confirm swaps every thumbs-down meal in one
    Claude call; if it fails, the plan confirms with them kept and the Week
    page shows a note.
  - Failed jobs never change data; the card, meal or meal list shows the
    error with Try again.
- SSE `GET /events` (one stream per household): `list.item`, `meal.updated`
  plus `meal-<id>`, `plan.updated`, `job.failed`. `/list/stream` now only
  re-renders on list changes.
- Web (wireframes P1 with the draft card, P3 Review without "Not cooked"):
  - Week: draft card (none / making / ready / failed), refreshes over SSE.
    The old "Make a new plan" form and the Week page's swap are gone.
  - `GET /plans/next`: full screen (no tab bar, "‹ Week"), Ask Claude,
    meal cards with thumbs up/down, swap, remove, "Swapped automatically if
    still disliked at 08:00", list preview line ("Show" opens
    `/plans/next/list-preview`), Confirm week. When another phone confirms,
    this one is sent to the week.
  - Routes as in the handoff, plus `GET /plans/next/card`,
    `GET /plans/next/meals`, `GET /meals/{id}/card` (the partials the SSE
    events refetch) and `POST /meals/{id}/delete` (no JavaScript).
- The JSON API is unchanged: `POST /api/plans` still makes and confirms a
  plan at once (now in one transaction).
- Tests: planner draft flow (card, votes, swap, failed swap, Ask Claude,
  preview, confirm once, carry-over, schedules, auto-confirm with and
  without a working Claude, cross-household), the job queue (dedupe, each
  job once with 4 workers, crash recovery), web routes + `/events`
  isolation, and Playwright: two phones review together (votes and swaps
  on the other phone within 2 s, both confirm, confirmed once).

**Known issues**

- Until window 3, the card's "Start review" opens the review directly (no
  rating step) and the review has no "Not cooked last week" section.
- Ask Claude matches kept meals by type and title; if Claude renames a meal
  it means to keep, that meal loses its votes.
- The time zone is not editable in the UI (default Europe/Copenhagen).
- The draft-ready email is window 4.

## Next: window 3 – Rating and recipe library
