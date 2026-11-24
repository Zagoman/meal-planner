# Gaps

Everything known to be missing, rough or different from the dev handoff,
in one place. Add a row when you find one; move it to **Fixed** (with the
commit) when it is done. `HANDOVER.md` says what each window built; this
file is the to-do list that outlives the windows.

IDs are stable: refer to a gap as `G-12` in commits and issues.

## Open

### Product behaviour

| ID | Area | Gap | Impact | Suggested fix | Found |
|---|---|---|---|---|---|
| G-1 | Rating | The rating step only covers the active plan. Once the next draft is confirmed, last week's plan is archived and its unrated meals can no longer be rated in the UI. | Ratings and "not cooked" are lost if a household confirms before rating (for example an auto-confirm at 08:00). | Rate the most recently archived plan when the active one was confirmed after the rating window, or keep a "rate last week" link for the archived plan until it is rated. | W3 |
| G-2 | Not cooked | The weekly draft is made at 18:00 the evening before shopping day, before the rating step. A meal of that draft can be one the household then marks not cooked. | That meal stays in the draft unless swapped. Later swaps, Ask Claude and drafts avoid it. Rare: Claude has to repeat last week's meal. | On marking a meal not cooked, flag a draft meal with the same recipe ("not cooked last week") on the review page, or swap it at auto-confirm like a thumbs-down. | W3 |
| G-3 | Rating | Ratings are one per meal for the household (whoever answered last), not one per member. | Two people who disagree overwrite each other. | Per-member outcomes with an average, if wanted. | W3 |
| G-4 | Rated 1 | A recipe with any rating of 1 is never suggested again, even if it was rated 5 later. | A one-off bad night bans a recipe for good. | Ban on the latest rating, or on an average ≤ 1.5; allow un-banning from the recipe page. | W3 |
| G-5 | Library | Every meal Claude plans is saved to the library (handoff rule), including drafts nobody confirmed and meals swapped away. There is no way to hide or delete a recipe. | The library fills with meals never cooked. | Sort by cooked/rated first (done), add an "archive" action, or only list recipes that were in a confirmed plan unless searching. | W3 |
| G-6 | Ask Claude | Meals Claude keeps are matched by meal type and title. If Claude renames a meal it means to keep, the meal is replaced and loses its votes. | Votes vanish after some Ask Claude requests. | Give Claude the meal ids and ask it to return them for kept meals. | W2 |
| G-7 | Add to this week | Ingredients already on the list are skipped, not merged: if the recipe needs more than the list has, the amount is not raised. | Can under-buy. | Merge amounts with the existing item's quantity (the builder's unit logic). | W3 |
| G-8 | Time zone | The household time zone (default Europe/Copenhagen) cannot be changed in the UI. | Only matters for households elsewhere. | A select on the Profile page (validated with `time.LoadLocation`). | W2 |
| G-9 | Quantities | Fractional counts show "need 0.5" next to a whole buying unit ("1 net · need 0.5"). | Looks odd. | Hide "need" below one unit, or round "need" for counted things. | W1 |
| G-10 | Draft card | With no draft, the card shows "Make next week's plan now" instead of nothing (handoff: "none" state is nothing). | Deliberate: otherwise there is no way to make a draft before the weekly schedule. | Keep, or move the button elsewhere. | W2 |
| G-11 | Rating page | The rating page does not update live on other phones (no SSE on it). | Two people rating at once each see only their own answers until reload. | Publish an outcome event and refetch `#outcome-{meal}` / `#rate-progress`. | W3 |

### Technical

| ID | Area | Gap | Impact | Suggested fix | Found |
|---|---|---|---|---|---|
| G-12 | Errors | Claude errors are wrapped twice: "generation failed: claude: claude: messages.new: …" (seen on the live server). | Only log and job-error noise. | Drop one of the "claude:" prefixes (`planner.ask` or the client). | W2 deploy |
| G-13 | List page | The List page still uses `/list/stream`, which sends the whole list as one partial; adding an item and Have it / Buy swap the whole `#list`. The handoff wants per-partial swaps (`#item-{id}`, `#section-{key}`, `#check-home`) driven by `/events`. | More data per change; works fine at this size. | Move the List page to `/events` (`list.item`) and per-item refetches. The stable ids are already there. | W1 |
| G-14 | API | The JSON API has no endpoints for Have it / Buy, single avoid entries, drafts, votes, outcomes or the library. `POST /api/plans` still makes and confirms a plan at once. | Scripts and integrations can't use the review flow. | Add them to `openapi.yaml` when an API client needs them. | W1–W3 |
| G-15 | Testing | The web tests and Playwright run against the in-memory fake (`internal/web/webfake`), which simplifies the rules (no avoid list or meal targets, simple swaps). Only the planner and API tests use Postgres and the real services. | A web-layer bug that depends on real service behaviour can slip through. | A Playwright run against the real server with a Postgres container and `claude.NewFake`. | W1 |
| G-16 | Deploy check | The handoff's "run the end-to-end test on the home server" isn't possible as written: the live site needs the household's own sign-in, which automation must not use. After each deploy only health, migrations, data and signed-out routes are checked. | Signed-in pages are verified on the household's phone, not automatically. | A staging stack on the home server with test accounts, or a smoke account. | W1 deploy |
| G-17 | Migrations | Migration 00002 normalizes old avoid entries in SQL without making the last word singular ("mushrooms" stays "mushrooms"). | None today (matching compares whole words; new entries use the app's normalizer). | Re-normalize avoid entries in Go once. | W1 |
| G-18 | Migrations | Rolling back 00004 deletes the back-filled and tweaked recipes; rolling back 00003 deletes drafts. | Only on a rollback. | Restore from the dump taken before each deploy. | W2, W3 |
| G-19 | Week start | Plans made before the review flow start on the Monday after they were made; drafts start on the shopping day. The two meet in `covers()` (fixed in `9b81fe9`). | None now; keep in mind when touching week logic. | Once no legacy plans are active, drop the special case. | W2 deploy |
| G-20 | Library search | The search returns at most 200 recipes and filters ingredients with `ILIKE` on JSON. | Fine for a household library. | Pagination and an index if it grows. | W3 |

## Deviations from the handoff (on purpose)

| What | Handoff | Here | Why |
|---|---|---|---|
| Plan status | draft, confirmed, archived | draft, **active**, archived (+ `confirmed_by`, `confirmed_at`) | Less churn; `active` means confirmed. |
| Shopping day | 0–6 | lowercase day name | Matches the existing convention. |
| Tick route | `POST /list/items/{id}/toggle` toggles | sets the posted state (`checked=true/false`); `/check` still works | Idempotent for the offline tick queue. |
| List route | `/list` | `/list` and `/` | Old links and the service worker cache keep working. |
| Library search trigger | `keyup changed delay:300ms` | `input changed delay:300ms` | Paste and autofill search too. |
| Meal card refresh | `meal.updated` | `meal.updated` and `meal-<id>` | Only the changed card refetches. |
| Extra partial routes | — | `GET /plans/next/card`, `/plans/next/meals`, `/plans/next/not-cooked`, `/meals/{id}/card`, `/recipes/{id}/tweak` | The partials the SSE events refetch. |
| No-JS delete | `DELETE /meals/{id}`, `/profile/avoid/{id}` | also `POST …/delete` | Forms can't send DELETE without JavaScript. |
| `POST /api/plans` | (not covered) | still makes and confirms a plan in one go | Keeps the API contract. |
| Weekly draft | made at 18:00 | skipped if that week already has a draft with Claude meals or a confirmed plan | A schedule must not overwrite a draft the household is reviewing. |

## Fixed

| ID | Gap | Fixed in |
|---|---|---|
| — | Old Monday plans blocked the weekly draft and made "make it now" target the week after. | `9b81fe9` (W2) |
| — | A deploy during a running job failed the job ("context canceled"); now requeued, and the server waits for the worker. | `d72783b` (W2) |
| — | Job queue compared this machine's clock with the database's, so jobs could sit unclaimed. | W2 (before commit) |
| — | Rating progress didn't update (out-of-band swap rendered as a bare attribute). | W3 (before commit) |
| — | Library filter chips widened the page at phone width and pushed the tab bar off-screen. | W3 (before commit) |
| — | The Add item form stacked into three rows and covered the list at phone width. | W1 (before commit) |
| — | Swap and remove icons drew wrong (joined SVG paths). | W2 (before commit) |
