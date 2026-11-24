-- name: CreatePlan :one
INSERT INTO plans (household_id, week_start, status, prompt, created_by)
VALUES ($1, $2, 'draft', $3, $4) RETURNING *;

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1 AND household_id = $2;

-- name: GetActivePlan :one
SELECT * FROM plans WHERE household_id = $1 AND status = 'active';

-- name: ArchiveActivePlan :exec
UPDATE plans SET status = 'archived' WHERE household_id = $1 AND status = 'active';

-- name: ActivatePlan :execrows
UPDATE plans SET status = 'active' WHERE id = $1 AND household_id = $2 AND status = 'draft';

-- name: DeleteDraftPlan :exec
DELETE FROM plans WHERE id = $1 AND household_id = $2 AND status = 'draft';

-- name: CreateMeal :one
INSERT INTO meals (household_id, plan_id, day, meal_type, position, title, protein, minutes, one_pot, servings,
                   after_baby_portion, recipe_id, added_from)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING *;

-- name: CountClaudeMeals :one
SELECT count(*) FROM meals WHERE plan_id = $1 AND household_id = $2 AND added_from = 'claude';

-- name: GetMeal :one
SELECT * FROM meals WHERE id = $1 AND household_id = $2;

-- name: ListMeals :many
-- Suggested day (meals without one last), then meal type, then position.
SELECT * FROM meals WHERE plan_id = $1 AND household_id = $2
ORDER BY array_position(ARRAY['monday','tuesday','wednesday','thursday','friday','saturday','sunday'], day) NULLS LAST,
         array_position(ARRAY['breakfast','lunch','dinner','baking','other'], meal_type),
         position, created_at;

-- name: DeleteMeal :exec
DELETE FROM meals WHERE id = $1 AND household_id = $2;

-- name: CreateIngredient :exec
INSERT INTO ingredients (household_id, meal_id, position, name, normalized_name, qty, unit, section)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListIngredientsForMeal :many
SELECT * FROM ingredients WHERE meal_id = $1 AND household_id = $2 ORDER BY position;

-- name: ListIngredientsForPlan :many
SELECT i.* FROM ingredients i
JOIN meals m ON m.id = i.meal_id AND m.household_id = i.household_id
WHERE m.plan_id = $1 AND i.household_id = $2
ORDER BY m.position, m.created_at, i.position;

-- name: UpdateIngredient :execrows
UPDATE ingredients SET name = $3, normalized_name = $4, qty = $5, unit = $6, section = $7
WHERE id = $1 AND household_id = $2;

-- name: CreateStep :exec
INSERT INTO steps (household_id, meal_id, position, text, timer_min, baby_portion)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListStepsForMeal :many
SELECT * FROM steps WHERE meal_id = $1 AND household_id = $2 ORDER BY position;

-- name: GetDraftPlan :one
-- The newest draft, if any.
SELECT * FROM plans WHERE household_id = $1 AND status = 'draft'
ORDER BY week_start DESC, created_at DESC LIMIT 1;

-- name: GetDraftPlanForWeek :one
SELECT * FROM plans WHERE household_id = $1 AND status = 'draft' AND week_start = $2;

-- name: LockPlan :one
SELECT * FROM plans WHERE id = $1 AND household_id = $2 FOR UPDATE;

-- name: CreateDraftPlan :one
INSERT INTO plans (household_id, week_start, status, prompt, created_by)
VALUES ($1, $2, 'draft', $3, $4) RETURNING *;

-- name: ConfirmDraftPlan :execrows
UPDATE plans SET status = 'active', confirmed_by = sqlc.narg(confirmed_by), confirmed_at = now(),
    auto_confirmed = sqlc.arg(auto_confirmed), confirm_note = sqlc.arg(confirm_note)
WHERE id = sqlc.arg(id) AND household_id = sqlc.arg(household_id) AND status = 'draft';

-- name: CountMeals :one
SELECT count(*) FROM meals WHERE plan_id = $1 AND household_id = $2;

-- name: MaxMealPosition :one
SELECT coalesce(max(position), -1)::int FROM meals WHERE plan_id = $1 AND household_id = $2;

-- name: SetMealPosition :exec
UPDATE meals SET position = $3 WHERE id = $1 AND household_id = $2;

-- name: ListSchedules :many
-- Every household's schedule settings, for the scheduler.
SELECT household_id, shopping_day, auto_confirm_time, time_zone FROM profiles;

-- name: UpsertVote :exec
INSERT INTO meal_votes (household_id, meal_id, member_id, vote) VALUES ($1, $2, $3, $4)
ON CONFLICT (meal_id, member_id) DO UPDATE SET vote = EXCLUDED.vote, updated_at = now();

-- name: DeleteVote :exec
DELETE FROM meal_votes WHERE meal_id = $1 AND member_id = $2 AND household_id = $3;

-- name: ListVotesForPlan :many
SELECT v.meal_id, v.member_id, v.vote, mb.name, mb.email
FROM meal_votes v
JOIN meals m ON m.id = v.meal_id AND m.household_id = v.household_id
JOIN members mb ON mb.id = v.member_id
WHERE m.plan_id = $1 AND v.household_id = $2
ORDER BY v.updated_at;

-- name: ListVotesForMeal :many
SELECT v.meal_id, v.member_id, v.vote, mb.name, mb.email
FROM meal_votes v JOIN members mb ON mb.id = v.member_id
WHERE v.meal_id = $1 AND v.household_id = $2
ORDER BY v.updated_at;

-- name: CreateActivePlan :one
-- Used inside the transaction that archives the previous active plan.
INSERT INTO plans (household_id, week_start, status, prompt, created_by, confirmed_by, confirmed_at)
VALUES ($1, $2, 'active', $3, $4, $4, now()) RETURNING *;

-- name: ReplaceMealContent :execrows
-- A swap keeps the meal's id, type and position; ingredients, steps and
-- votes are deleted and inserted again by the caller.
UPDATE meals SET day = $3, title = $4, protein = $5, minutes = $6, one_pot = $7, servings = $8,
    after_baby_portion = $9, added_from = 'claude', recipe_id = NULL
WHERE id = $1 AND household_id = $2;

-- name: DeleteIngredientsForMeal :exec
DELETE FROM ingredients WHERE meal_id = $1 AND household_id = $2;

-- name: DeleteStepsForMeal :exec
DELETE FROM steps WHERE meal_id = $1 AND household_id = $2;

-- name: DeleteVotesForMeal :exec
DELETE FROM meal_votes WHERE meal_id = $1 AND household_id = $2;

-- name: DeleteDraftForWeek :exec
DELETE FROM plans WHERE household_id = $1 AND week_start = $2 AND status = 'draft';
