# Handover

Where the build stands against the dev handoff (five windows). Update this at
the end of every window. Open gaps and deviations from the handoff are
tracked in [GAPS.md](GAPS.md); the "Known issues" below are the snapshot at
the time of each window.

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

**Deployed** 2026-11-24, now `d72783b10daa`. Dump first:
`mealplanner-20261124-193441.dump`. Migration 3 applied; existing plans got
`confirmed_at`, the profile got time zone Europe/Copenhagen (shopping day
Sunday, auto-confirm 08:00). The first weekly draft is due Saturday 28 Nov
18:00 for Sunday 29 Nov.

Two fixes found on the live server, deployed the same evening:

- `9b81fe9`: plans made before the review flow start on the Monday after
  they were made (the live plan: 30 Nov), which looked like they covered
  the Sunday shopping week and would have skipped the weekly draft. Only a
  plan starting on the shopping weekday covers a week now.
- `d72783b`: a job cut off by a deploy failed with "context canceled"; it
  is now requeued, and the server waits for the worker on shutdown. One
  draft job (19:37, for 6 Dec) failed this way before the fix; the Week card
  shows "Try again".

**Known issues**

- Until window 3, the card's "Start review" opens the review directly (no
  rating step) and the review has no "Not cooked last week" section.
- Ask Claude matches kept meals by type and title; if Claude renames a meal
  it means to keep, that meal loses its votes.
- The time zone is not editable in the UI (default Europe/Copenhagen).
- The draft-ready email is window 4.

## Window 3 – Rating and recipe library (branch `window-3-rating-library`)

Open questions answered with the handoff's defaults: a tweak is saved as a
new version and the original kept; "Add to next week" with no draft makes
a draft holding that meal.

**Built**

- Migration `00004_outcomes_library.sql`:
  - `meal_outcomes` replaces `feedback`: one per meal (cooked with a 1–5
    rating, or not cooked), whoever answered last. The old ratings are
    copied (latest per meal); `feedback` is dropped.
  - `recipes`: `source`, `parent_recipe_id`, `meal_type`, `title_key`;
    Claude's recipes are unique per household, type and title.
  - Every existing meal is back-filled into the library and linked
    (`meals.recipe_id`).
- Every meal Claude plans (draft, swap, Ask Claude, auto-confirm, the API's
  generate) is saved to the library and linked; a title suggested again
  reuses its recipe.
- Recipes someone rated 1 and the meals of the active plan marked not
  cooked are listed in every prompt as "never plan" and rejected by
  validation (Ask Claude may keep meals already in the draft; a tweak may
  start from a disliked recipe).
- `service.Outcomes` (rating step) and `service.Library` (search, detail,
  tweak job, add to next week, add to this week), in `planner/library.go`.
  `Feedback.Rate` (the API) now writes the outcome.
  - A new draft keeps meals added to it by hand (library, added back) and
    tells Claude not to plan them again; a scheduled draft fills in a draft
    that holds only such meals.
  - Add to this week: ingredients not on the active list yet, as unticked
    manual items with their amount; water never.
- Web (wireframes P2, Not cooked of P3, L1, L4):
  - `GET /plans/current/rate`: full screen, "Step 1 of 2" when a draft
    waits, 1–5 and Not cooked per meal, notes, progress (`#rate-progress`,
    swapped out of band), Skip / Continue to draft. The draft card's
    "Start review" opens it while a meal of this week is unanswered.
  - Review: "Not cooked last week" (`#not-cooked`) with Add back
    (`POST /plans/next/meals`).
  - `GET /recipes?q=&filter=`: search (title or ingredient) and filter
    chips; htmx gets `#recipe-results`. The search fires on `input` (not
    only `keyup`, so pasting and autofill search too).
  - `GET /recipes/{id}`: pills, latest note, Tweak with Claude (`#tweak`,
    refreshes on `recipe-<id>` over SSE), ingredients, steps, Add to next
    week / Add to this week (toast).
- Tests: planner (rate a week, not-cooked comes back only when added,
  rated-1 never suggested, library search/filters, tweak + add to this
  week, add to next week without a draft, cross-household), migration data
  copy, web routes for all of it, and Playwright (rate the week, add the
  not-cooked meal back, search, tweak, add to this week).

**Deployed** 2026-11-24 as `4eaf1f68511b`. Dump first:
`mealplanner-20261124-213336.dump`. Migration 4 applied: the one old rating
became one outcome, `feedback` is gone, all 24 meals are in the library (24
recipes) and linked. Plans unchanged (1 draft, 1 active, 4 archived). The
live draft was made before this deploy, so it was made without the
rated-1/not-cooked rules (see G-2 in GAPS.md).

**Known issues** (tracked in GAPS.md)

- A draft made before the rating step (the 18:00 weekly draft) can already
  contain a meal that is then marked not cooked; only later swaps, Ask
  Claude and drafts avoid it. Rare: Claude would have to repeat last week's
  meal.
- Ratings are per household, not per member.
- "Add recipe" (L2) is window 5; the library has no photos yet.

## Window 4 – Households and email (branch `window-4-households-email`)

Defaults used: invite links last 7 days; email sign-in is added next to
passwords (password sign-in is still there, behind "Sign in with a
password").

**Built**

- Migration `00005_login_links_setup.sql`: `login_tokens` (single use, 15
  minutes, hash only, optional `invite_id`) and `profiles.setup_done`
  (existing households: done).
- `internal/mail`: Resend over HTTPS (no tracking), a log-only mailer when
  `RESEND_API_KEY` is empty (never logs the link), a recorder for tests.
  Config: `RESEND_API_KEY`, `EMAIL_FROM` (default
  "Meal Planner <meals@liluhome.dk>").
- Auth: `RequestLoginLink` (same answer for every address, at most one link
  a minute per member), `SignInWithLink`, `InvitePreview`, `Join` (creates
  the member without a password; the membership is added when the emailed
  link is used, and that uses up the invite).
- Households: `Create` returns a one-time admin invite (`/join/{token}`);
  `admin_email` is optional. Profiles: `Setup` saves the four questions
  (never-eat foods go to the avoid list with source `setup`) and marks the
  household set up.
- Planner: "Next week's plan is ready" goes to every member for the weekly
  draft and for a household's first plan (after setup): meal count,
  confirm day and time, one link to `/plans/next`.
- Web (wireframes H1, H2): login offers "Email me a sign-in link";
  `/auth/request` always says "check your inbox"; `/auth/{token}` shows a
  Sign in button (POST uses the link); `/join/{token}` (old `/invite/...`
  links redirect); `/setup` with steppers and a day picker; members of a
  household that isn't set up are sent to `/setup`; after setup the review
  page shows "Making your plan…" until the first draft is ready;
  `POST /households/invites`, `POST /households/{id}/switch`; Profile shows
  the household's members, invite (admins) and switching.
- Tests: sign-in links (single use, expiry, throttle, no account
  disclosure, hash only), join (membership only on link use, invite used
  once), create with invite; `internal/server/household_flow_test.go`
  runs the "done when" over HTTP with the real services: a new household
  joins from an invite, signs in by link, answers the four questions, gets
  the draft email, confirms and ticks the whole list; then household A
  calls **every web route with an id** (found by walking the router) with
  each of B's ids and never gets a success with content, and receives no
  SSE events.

**Deployed** 2026-11-25 as `6fdd950179ee`. Dump first:
`mealplanner-20261125-094829.dump`. Migration 5 applied; the existing
household is marked set up (no setup questions). The app logs "RESEND_API_KEY
is not set: emails are not sent" until the email steps below are done, and
`make deploy` warns that the server's `compose.yml` differs until the
homelab patch is applied. Plans unchanged (1 draft, 1 active, 5 archived).

**Needs you before email works** (G-21): a Resend account with sending
access for liluhome.dk, the SPF/DKIM/DMARC DNS records Resend gives you,
`vault_mealplanner_resend_api_key` in the Ansible vault, and
`deploy/homelab/window4-email.patch` applied to the homelab repo.

**Known issues**: see G-21 to G-25 in GAPS.md.

## Next: window 5 – Recipe import and cooking polish
