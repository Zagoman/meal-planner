-- name: CreateRecipe :one
INSERT INTO recipes (household_id, source_meal_id, title, protein, minutes, one_pot, servings, data, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *;

-- name: GetRecipe :one
SELECT * FROM recipes WHERE id = $1 AND household_id = $2;

-- name: ListRecipes :many
SELECT * FROM recipes WHERE household_id = $1 ORDER BY created_at DESC;

-- name: DeleteRecipe :exec
DELETE FROM recipes WHERE id = $1 AND household_id = $2;

-- name: UpsertFeedback :one
INSERT INTO feedback (household_id, meal_id, member_id, rating, note)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (meal_id, member_id) DO UPDATE SET rating = EXCLUDED.rating, note = EXCLUDED.note, created_at = now()
RETURNING *;

-- name: ListRecentFeedback :many
-- Feedback with the meal it rates, for the prompt ("last 8 weeks").
SELECT f.rating, f.note, f.created_at, m.title, m.protein
FROM feedback f
JOIN meals m ON m.id = f.meal_id AND m.household_id = f.household_id
WHERE f.household_id = $1 AND f.created_at >= sqlc.arg(since)
ORDER BY f.created_at DESC;

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
