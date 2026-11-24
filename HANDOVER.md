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
- Swap on the Week page works only for a dinner with a suggested day
  (`SwapMeal` is still by day). Window 2 swaps any meal by id.
- Generation is still synchronous in the request (up to 60 s). Window 2 moves
  it to the `jobs` worker.
- The migration's SQL normalization of old avoid entries does not make the
  last word singular ("mushrooms" stays "mushrooms"); matching still works
  because validation compares whole words, and the app normalizes new
  entries.
- Fractional counts still show "need 0.5" next to a whole buying unit (for
  example "1 net · need 0.5").
- No JSON API endpoints yet for Have it / Buy or for adding or removing one
  avoid entry (the profile `PUT` replaces the whole list).

## Next: window 2 – Draft and confirm

`plans.status`/`confirmed_*`, `meal_votes`, `jobs` table and worker, SSE
`GET /events` with per-partial events, draft card, review page, swap by meal
id, Ask Claude, confirm and auto-confirm, weekly draft schedule. Open
questions for it: thumbs-down rule at auto-confirm, and whether the weekly
draft time is a household setting.
