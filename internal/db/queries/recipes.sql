-- name: CreateRecipe :one
INSERT INTO recipes (household_id, source_meal_id, title, protein, minutes, one_pot, servings, data, created_by,
                     source, parent_recipe_id, meal_type, title_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, sqlc.narg(parent_recipe_id), $11, $12) RETURNING *;

-- name: UpsertClaudeRecipe :one
-- A meal Claude suggests again reuses its recipe (same type and title).
INSERT INTO recipes (household_id, source_meal_id, title, protein, minutes, one_pot, servings, data, created_by,
                     source, meal_type, title_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'claude', $10, $11)
ON CONFLICT (household_id, meal_type, title_key) WHERE source = 'claude' AND parent_recipe_id IS NULL
DO UPDATE SET title = recipes.title
RETURNING id;

-- name: GetRecipe :one
SELECT * FROM recipes WHERE id = $1 AND household_id = $2;

-- name: ListRecipes :many
SELECT * FROM recipes WHERE household_id = $1 ORDER BY created_at DESC;

-- name: DeleteRecipe :exec
DELETE FROM recipes WHERE id = $1 AND household_id = $2;

-- name: InsertUsage :exec
INSERT INTO usage_log (household_id, member_id, kind, model, input_tokens, output_tokens)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: UsageByHousehold :many
SELECT h.id AS household_id, h.name,
       count(u.id)::bigint AS calls,
       coalesce(sum(u.input_tokens), 0)::bigint AS input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint AS output_tokens
FROM households h
LEFT JOIN usage_log u ON u.household_id = h.id AND u.created_at >= sqlc.arg(since)
GROUP BY h.id, h.name
ORDER BY h.name;

-- name: SearchRecipes :many
-- The library with ratings: q matches the title or an ingredient.
SELECT r.id, r.title, r.meal_type, r.minutes, r.source, r.parent_recipe_id, r.created_at,
       coalesce(avg(o.rating), 0)::float8 AS rating,
       count(o.rating)::int AS ratings,
       (count(o.meal_id) FILTER (WHERE o.status = 'cooked'))::int AS cooked
FROM recipes r
LEFT JOIN meals m ON m.recipe_id = r.id AND m.household_id = r.household_id
LEFT JOIN meal_outcomes o ON o.meal_id = m.id AND o.household_id = m.household_id
WHERE r.household_id = sqlc.arg(household_id)
  AND (sqlc.arg(q)::text = ''
       OR r.title ILIKE '%' || sqlc.arg(q) || '%'
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(r.data -> 'ingredients') i
                  WHERE i ->> 'name' ILIKE '%' || sqlc.arg(q) || '%'))
  AND (sqlc.arg(meal_type)::text = '' OR r.meal_type = sqlc.arg(meal_type))
  AND (NOT sqlc.arg(own)::bool OR r.source <> 'claude')
GROUP BY r.id
HAVING (NOT sqlc.arg(rated4)::bool OR avg(o.rating) >= 4)
ORDER BY (count(o.meal_id) FILTER (WHERE o.status = 'cooked')) DESC, lower(r.title)
LIMIT 200;

-- name: RecipeStats :one
SELECT coalesce(avg(o.rating), 0)::float8 AS rating,
       count(o.rating)::int AS ratings,
       (count(o.meal_id) FILTER (WHERE o.status = 'cooked'))::int AS cooked,
       coalesce((SELECT o2.note FROM meal_outcomes o2 JOIN meals m2 ON m2.id = o2.meal_id AND m2.household_id = o2.household_id
                 WHERE m2.recipe_id = sqlc.arg(recipe_id) AND o2.household_id = sqlc.arg(household_id) AND o2.note <> ''
                 ORDER BY o2.at DESC LIMIT 1), '')::text AS latest_note
FROM meals m JOIN meal_outcomes o ON o.meal_id = m.id AND o.household_id = m.household_id
WHERE m.recipe_id = sqlc.arg(recipe_id) AND m.household_id = sqlc.arg(household_id);

-- name: BannedRecipeTitles :many
-- Recipes someone rated 1: never suggested again.
SELECT DISTINCT r.meal_type, r.title
FROM recipes r
JOIN meals m ON m.recipe_id = r.id AND m.household_id = r.household_id
JOIN meal_outcomes o ON o.meal_id = m.id AND o.household_id = m.household_id
WHERE r.household_id = $1 AND o.rating = 1;

-- name: UpsertOutcome :one
INSERT INTO meal_outcomes (household_id, meal_id, status, rating, note, member_id, at)
VALUES ($1, $2, $3, sqlc.narg(rating), $4, sqlc.narg(member_id), now())
ON CONFLICT (meal_id) DO UPDATE SET status = EXCLUDED.status, rating = EXCLUDED.rating,
    note = EXCLUDED.note, member_id = EXCLUDED.member_id, at = now()
RETURNING *;

-- name: GetOutcome :one
SELECT * FROM meal_outcomes WHERE meal_id = $1 AND household_id = $2;

-- name: DeleteOutcome :exec
DELETE FROM meal_outcomes WHERE meal_id = $1 AND household_id = $2;

-- name: ListOutcomesForPlan :many
SELECT o.* FROM meal_outcomes o
JOIN meals m ON m.id = o.meal_id AND m.household_id = o.household_id
WHERE m.plan_id = $1 AND o.household_id = $2;

-- name: ListRecentFeedback :many
-- Rated meals with the meal they rate, for the prompt ("last 8 weeks").
SELECT o.rating::int AS rating, o.note, o.at AS created_at, m.title, m.protein
FROM meal_outcomes o
JOIN meals m ON m.id = o.meal_id AND m.household_id = o.household_id
WHERE o.household_id = $1 AND o.at >= sqlc.arg(since) AND o.rating IS NOT NULL
ORDER BY o.at DESC;

-- name: ListNotCooked :many
-- Meals of a plan marked not cooked whose recipe is not in another plan.
SELECT m.id AS meal_id, m.recipe_id, m.title, m.meal_type
FROM meals m JOIN meal_outcomes o ON o.meal_id = m.id AND o.household_id = m.household_id
WHERE m.plan_id = sqlc.arg(plan_id) AND m.household_id = sqlc.arg(household_id) AND o.status = 'not_cooked'
  AND m.recipe_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM meals d WHERE d.plan_id = sqlc.arg(other_plan_id) AND d.household_id = m.household_id
                  AND d.recipe_id = m.recipe_id)
ORDER BY m.position;

-- name: SetMealRecipe :exec
UPDATE meals SET recipe_id = $3 WHERE id = $1 AND household_id = $2;

-- name: LatestRecipeJob :one
SELECT * FROM jobs WHERE household_id = $1 AND kind = 'tweak' AND payload ->> 'recipe_id' = sqlc.arg(recipe_id)::text
ORDER BY created_at DESC LIMIT 1;
