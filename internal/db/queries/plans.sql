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
INSERT INTO meals (household_id, plan_id, day, title, protein, minutes, one_pot, servings, after_baby_portion)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *;

-- name: GetMeal :one
SELECT * FROM meals WHERE id = $1 AND household_id = $2;

-- name: GetMealByDay :one
SELECT * FROM meals WHERE plan_id = $1 AND household_id = $2 AND day = $3;

-- name: ListMeals :many
SELECT * FROM meals WHERE plan_id = $1 AND household_id = $2
ORDER BY array_position(ARRAY['monday','tuesday','wednesday','thursday','friday','saturday','sunday'], day);

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
ORDER BY m.day, i.position;

-- name: UpdateIngredient :execrows
UPDATE ingredients SET name = $3, normalized_name = $4, qty = $5, unit = $6, section = $7
WHERE id = $1 AND household_id = $2;

-- name: CreateStep :exec
INSERT INTO steps (household_id, meal_id, position, text, timer_min, baby_portion)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListStepsForMeal :many
SELECT * FROM steps WHERE meal_id = $1 AND household_id = $2 ORDER BY position;
