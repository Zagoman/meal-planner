# Gaps

Everything known to be missing, rough or different from the dev handoff
and from the design handover (Hvad skal vi ha'?; "Found" `DA` is design
step A, `DB` step B, `DC` step C), in one place. Add a row when you find one; move it to **Fixed**
(with the commit) when it is done. `HANDOVER.md` says what each window built; this
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
| G-21 | Email | Emails go out only once a Resend API key is set and liluhome.dk has SPF, DKIM and DMARC records (your setup). Until then sign-in links and draft emails are only logged (without the link); password sign-in still works. | No sign-in by link, no draft email. | Resend account, DNS records, vault key, apply `deploy/homelab/window4-email.patch` (deploy/README.md, "Email"). | W4 |
| G-22 | Sign-in | `/auth/request` and `/join/{token}` have no per-IP rate limit; only one link per member per minute is sent. | Someone could make the server send many links to many addresses it already knows (not more than one a minute each). | A small per-IP limiter on the two routes. | W4 |
| G-23 | Accounts | Signing in by link does not sign out other sessions, and there is no list of active sessions or a "sign out everywhere". | A lost phone stays signed in up to 30 days (changing the password signs out the others). | A sessions list on the account page. | W4 |
| G-24 | Join | The old invite route still lets a new person sign up with a password (`POST /invite/{token}/signup`); the join page itself only offers email links. | Two ways in; harmless. | Remove the password sign-up once nobody uses old links. | W4 |
| G-25 | Setup | Only the household's first plan and the weekly draft send the "draft ready" email; a plan made with "make it now" doesn't. | Deliberate (the person is looking at the screen), but other members aren't told. | Email the other members only. | W4 |
| G-26 | Import | Photos are not turned upright from their EXIF orientation before they go to Claude. | A sideways phone photo is sent sideways; Claude usually still reads it. | Apply the EXIF orientation in `imagefit.Fit`. | W5 |
| G-27 | Import | Recipe drafts that are never saved or discarded stay in `recipe_drafts` (without the photo once read). | A little clutter in the database; not shown anywhere. | Delete drafts older than 30 days in the scheduler. | W5 |
| G-28 | Library | A saved recipe can't be edited afterwards (only tweaked with Claude, which makes a new version). | A typo found later stays, or needs a tweak. | An "Edit" link reusing the check form. | W5 |
| G-29 | Recipe page | The C1 wireframe's "Swap ingredient" button is not built (the service has `Substitute`, reachable through the JSON API only). | Swapping one ingredient needs Ask Claude or a tweak. | A small form on the meal page calling `Plans.Substitute`. | W5 |
| G-30 | Cook mode | A running timer stops showing when you move to another step (the alarm still sounds). | You have to step back to see the time left. | Keep running timers in a bar above the step. | W5 |
| G-31 | Cook mode | The Screen Wake Lock could not be checked in the preview browser (it fell back to "may turn the screen off"). | Works on phones that support it; unverified here. | Check on a phone. | W5 |
| G-32 | Offline ticks | "Last tick wins" uses the phone's clock for offline ticks (clamped to the last 7 days and never later than the server's now). | A phone with a clock far behind can lose a tick it made later. | Fine for a household; a vector clock would be overkill. | W5 |
| G-39 | Domain | The app stays on meals.liluhome.dk; the brand domain (hvadskalviha.app or hvadskalvihave.dk) is still to be checked and bought. | Name and domain differ. | Decide the domain; move `BASE_URL`, email sender and Caddy. | DA |
| G-40 | Setup | Cuisines are a fixed list of eight (`domain.Cuisines`); a family can't type its own. | "Thai" or "Japanese" fans pick "Asian". | A free-text "Other" chip, cleaned like the avoid list. | DB |
| G-41 | Plan screen | Until the week is rated, the plan screen is long: five rating cards before the draft (it folds to one line once all are answered). | More scrolling on the first visit of the week. | Fold rated cards as they are answered. | DB |
| G-42 | Recipe image | The dish type is guessed from the title (English and Danish words), baking, fish and one-pot; breakfasts and anything unmatched get the oven dish drawing. | A porridge shows an oven dish. | Ask for a dish type in the meal schema, or add drawings (bowl, salad, sandwich). | DC |
| G-43 | Recipe image | Photos show on the meal and recipe pages only, not as thumbnails in the week or the library. | Less visual lists. | Small thumbnails once the photo endpoint serves a smaller size. | DC |
| G-44 | Backups | Recipe photos (about 100–400 KB each) are in the database, so dumps grow with them. | Bigger nightly dumps. | Fine for a family; move to files if it grows. | DC |
| G-45 | FAQ | No FAQ page. The design handover says AI is mentioned "in onboarding and FAQ"; only the setup note exists. | Nowhere to read how plans are made or what the family's details are used for after setup. | A short FAQ page in the brand voice, linked from Profile. | DC |
| G-46 | Brand | The new look is checked in the preview browser only, not on a phone (fonts, speckle, safe areas, the bar colour, the home-screen icon). | Something may look off on a real device. | Open it on an iPhone and an Android phone; re-add the home-screen app. | DC |
| G-47 | Test fake | The e2e fake gives a new draft the same week start as the current plan, so both show the same week number in the preview. | Preview and e2e only; the real planner is right. | Start fake drafts on the next shopping day. | DC |

### Later (the design handover says not yet)

Not gaps to fix now; listed so they aren't lost. The dev handoff's own list
is "Don't build yet" in `CLAUDE.md`.

| What | Handover note |
|---|---|
| Light/dark setting | `data-theme` on `<html>` already forces a theme; no setting in Profile yet. |
| Danish UI | Brand lines are Danish; the UI is English until it is translated (no i18n yet). |
| Per-person never-eat lists | The avoid list is household-wide for now. |
| Landing page | The lockups and `display` type are meant for it; the app has none (family use only). |
| Social, validation | "Rated by a 1-year-old" series, showing the logo to family users: not code. |

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
| Sign-in link | `GET /auth/{token}` sets the session | `GET` shows a "Sign in" button; `POST /auth/{token}` uses the link | Mail scanners open links; a single-use link would be used up before the person taps it. |
| Join | creates the member and joins | creates the member (no password); the membership is added when the emailed link is used | Typing someone else's email on an invite page can't add them to a household. |
| Never-eat foods (setup) | chips, add with Enter | one text field, comma-separated | Works without JavaScript; they become avoid-list chips on Profile. |
| Passwords | email links only | email links first; password sign-in kept behind "Sign in with a password" | Agreed at the start; email depends on the Resend setup (G-21). |
| Invite links | `/join/{token}` | `/join/{token}`; old `/invite/{token}` links redirect there | Links already sent keep working. |
| Create household | `POST /api/households` | `admin_email` optional; the response's `invite.url` is the one-time admin link | The first admin joins like everyone else. |
| Add a recipe (L2) | not routed | `GET /recipes/add` | The library's "Add recipe" button needs a page. |
| Manual entry | `GET /recipes/new` | also `POST /recipes` to save it | The handoff lists only the page. |
| Import status | redirect when the job finishes | `GET /recipes/drafts/{id}/status` (SSE `recipe-<id>` and every 5 s), reloads the page when done; `POST /recipes/drafts/{id}/discard` | The reading page needs something to poll; a draft needs a way out. |
| Recipe form amounts | "500 g" in one field | amount and unit in two fields | No parsing guesswork; Claude fills both. |
| Never-eat link | `GET /avoid/confirm` returns the sheet | the sheet for htmx, a full confirm page without JavaScript (`back=` returns to the recipe) | Works without JavaScript. |
| Offline tick | last tick per item | each tick carries the time it was made (`at`) and applies only if it is the latest | "Last tick" across phones, not only within one phone's queue. |
| Cook mode | `GET /recipes/{id}/cook` | also `GET /meals/{id}/cook` (the meal page's "Start cooking") | Week meals are meals, not library recipes. |
| Rating (design) | thumbs up / down | 1–5 stars kept | Decided with the owner: finer ratings; 1 still bans a recipe. |
| AI use (design) | planning and tweaking saved recipes only | also recipe import (photo, paste, link) | Decided with the owner: import can't read a photo without it. |
| Fonts (design) | Google Fonts `<link>` | self-hosted woff2 in `web/static/fonts` (OFL) | Decided with the owner: works offline in the shop, no call to Google. |
| Component classes (design) | `hsv-*` classes everywhere | `hvadskalviha.css` has the `hsv-*` components; existing class names are restyled to match in `app.css`. MealRow, Chip, Mark and Loader use `hsv-*` directly. Shopping rows stay buttons, not `<label>` + checkbox | Keeps the offline tick queue and every test selector working. |
| Email mark (design) | mark SVG in emails | the name as serif text, no image | An image would be fetched from the server when the email is opened. |
| Navigation (design) | not specified | the bottom tab bar stays fixed; action bars at the bottom are gone (actions end the content) | Navigation is not an action bar; the handover's rule is about actions. |
| Setup (design) | one question per screen; a "Skip" button on the cuisines step | one page with five numbered questions; cuisines marked optional, nothing ticked means skipped | Works without JavaScript and keeps the setup to one submit. |
| Plan screen (design) | titled "This week", rating shown as thumbs | `/plans/next` titled "Next week" ("Week N · Draft"), 1–5 ratings; the week page keeps "This week" for the confirmed plan with "Week N · Confirmed" | The draft and the week being cooked are different plans. |
| Tick save failed (design) | revert the tick, then "We'll try again" | the tick stays (dashed, queued) and is retried; the line shows under the row until it saves | Reverting and retrying at once would show the wrong state while it retries. |
| Empty plan (design) | heading + primary "Make a plan" | heading; the draft card above has the one "Make next week's plan now" button | One primary per screen. |
| Dish type (design) | a recipe's dish type picks its drawing | worked out from the meal (`domain.DishTypeOf`), not stored or asked of the AI | No change to the meal schema or the prompt; good enough for six drawings. |
| Photo upload (design) | "Add your photo" secondary button | the button opens the camera or library and uploads at once (no-JS: an Upload button); "Remove photo" is a link | One tap, like the recipe import. |

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
| — | "Add to this week" with another household's recipe id answered 409 instead of 404 when the caller had no plan (found by the route walk in the window 4 isolation test). | W4 (before commit) |
| — | The setup questions showed "1 of 4" under the question instead of above it. | W4 (before commit) |
| — | The avoid sheet stayed open after "Avoid …" (the close handler was on the button; htmx fires on the form). | W5 (before commit) |
| — | The avoid toast started with a lower-case ingredient name. | W5 (before commit) |
| — | A 31 MB local build of the server binary (`server`, from `go build ./cmd/...`) was committed in `6fdd950` and pushed. Removed and ignored; it is still in the git history unless that is rewritten. | after W4 |
| G-33 | The plan screen is one pass: rating, then Not cooked last week (updates as meals are marked), then the draft and Confirm plan; the old rating page redirects there while a draft waits. | design B (before commit) |
| G-34 | The design handover's state copy: first guess, empty plan, empty list, list done, generation failed, tick save failed. | design B (before commit) |
| G-35 | Setup has the baby-portions switch (off by default), an optional cuisines step (migration 00007, used in the prompt, editable in Profile) and the AI note. | design B (before commit) |
| G-37 | A new or failed plan arriving over SSE is announced (`#announce`); a page change moves focus to its title. | design B (before commit) |
| G-38 | One primary button per screen: the cook-mode timer, rating saves and Profile's secondary forms are secondary. | design B (before commit) |
| G-36 | Recipes show the line drawing for their dish type (worked out from the meal) until the family adds a photo; "Add your photo" on the meal and recipe pages, stored resized in `recipe_photos` (migration 00008), served only to the household, removable. | design C (before commit) |
| — | The homelab email patch and `deploy/README.md` still sent email as "Meal Planner" (the app's own default was renamed). | design C (before commit) |
| — | Week numbers used the plan's first day, so a week starting on Sunday got the previous ISO week (the email said week 48 for a week that is mostly week 49). `domain.WeekNumber` counts from mid-week. | design B (before commit) |
| — | A timeout during the database reads of a plan generation was not reported as a generation failure (`domain.ErrGeneration`); `TestGenerateClaudeErrorAndTimeout` failed now and then on `main`. | design B (before commit) |
| — | Tapping a meal on the week page (or the draft card's link) opened a blank page: the links inherited `hx-select="#week"` from the SSE container. SSE containers now set `hx-disinherit="*"`; e2e test added. Was live in production (checked on `main`). | design A (before commit) |
| — | After the restyle, week meal rows were centred and the recipe page's baby step split into two columns (old `.meal-link`, `.meal` and step rules). | design A (before commit) |
