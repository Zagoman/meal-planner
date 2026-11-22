-- name: ListItemsForPlan :many
SELECT * FROM list_items WHERE plan_id = $1 AND household_id = $2 ORDER BY created_at, name;

-- name: GetListItem :one
SELECT * FROM list_items WHERE id = $1 AND household_id = $2;

-- name: UpsertGeneratedListItem :one
-- Rebuilds keep ticks: on conflict only the text/section change, never checked.
INSERT INTO list_items (household_id, plan_id, name, normalized_name, qty_text, need_text, section, is_manual)
VALUES ($1, $2, $3, $4, $5, $6, $7, false)
ON CONFLICT (plan_id, normalized_name) WHERE NOT is_manual DO UPDATE SET
    name = EXCLUDED.name,
    qty_text = EXCLUDED.qty_text,
    need_text = EXCLUDED.need_text,
    section = EXCLUDED.section,
    updated_at = now()
RETURNING *;

-- name: DeleteStaleGeneratedListItems :exec
DELETE FROM list_items
WHERE plan_id = $1 AND household_id = $2 AND NOT is_manual
  AND NOT (normalized_name = ANY(sqlc.arg(keep)::text[]));

-- name: AddManualListItem :one
INSERT INTO list_items (household_id, plan_id, name, normalized_name, section, is_manual)
VALUES ($1, $2, $3, $4, $5, true) RETURNING *;

-- name: SetListItemChecked :one
UPDATE list_items SET
    checked = sqlc.arg(checked),
    checked_by = CASE WHEN sqlc.arg(checked)::boolean THEN sqlc.narg(checked_by)::uuid END,
    checked_at = CASE WHEN sqlc.arg(checked)::boolean THEN now() END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND household_id = sqlc.arg(household_id)
RETURNING *;

-- name: DeleteListItem :execrows
DELETE FROM list_items WHERE id = $1 AND household_id = $2;
