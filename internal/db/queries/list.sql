-- name: ListItemsForPlan :many
-- Cleared items (#6, "Clear ticked") are left out; Undo brings them back.
SELECT * FROM list_items WHERE plan_id = $1 AND household_id = $2 AND cleared_at IS NULL ORDER BY created_at, name;

-- name: GetListItem :one
SELECT * FROM list_items WHERE id = $1 AND household_id = $2;

-- name: UpsertGeneratedListItem :one
-- Rebuilds keep ticks: on conflict only the text/section change, never checked.
-- An item that leaves the pantry forgets its Have it / Buy answer.
INSERT INTO list_items (household_id, plan_id, name, normalized_name, qty_text, need_text, section, check_home, is_manual)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, false)
ON CONFLICT (plan_id, normalized_name) WHERE NOT is_manual DO UPDATE SET
    name = EXCLUDED.name,
    qty_text = EXCLUDED.qty_text,
    need_text = EXCLUDED.need_text,
    section = EXCLUDED.section,
    check_home = EXCLUDED.check_home,
    have_it = CASE WHEN EXCLUDED.check_home THEN list_items.have_it END,
    updated_at = now()
RETURNING *;

-- name: DeleteStaleGeneratedListItems :exec
DELETE FROM list_items
WHERE plan_id = $1 AND household_id = $2 AND NOT is_manual
  AND NOT (normalized_name = ANY(sqlc.arg(keep)::text[]));

-- name: AddManualListItem :one
INSERT INTO list_items (household_id, plan_id, name, normalized_name, section, is_manual, added_by)
VALUES ($1, $2, $3, $4, $5, true, $6) RETURNING *;

-- name: SetListItemChecked :one
UPDATE list_items SET
    checked = sqlc.arg(checked),
    checked_by = CASE WHEN sqlc.arg(checked)::boolean THEN sqlc.narg(checked_by)::uuid END,
    checked_at = CASE WHEN sqlc.arg(checked)::boolean THEN now() END,
    tick_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND household_id = sqlc.arg(household_id)
RETURNING *;

-- name: SetListItemCheckedAt :one
-- A tick made at tick_at (an offline tick sent later). It applies only if
-- no later tick of the item was applied: the last tick wins. The row is
-- returned either way.
UPDATE list_items SET
    checked = CASE WHEN tick_at IS NULL OR tick_at <= sqlc.arg(tick_at)::timestamptz THEN sqlc.arg(checked)::boolean ELSE checked END,
    checked_by = CASE WHEN tick_at IS NULL OR tick_at <= sqlc.arg(tick_at)::timestamptz
        THEN (CASE WHEN sqlc.arg(checked)::boolean THEN sqlc.narg(checked_by)::uuid END) ELSE checked_by END,
    checked_at = CASE WHEN tick_at IS NULL OR tick_at <= sqlc.arg(tick_at)::timestamptz
        THEN (CASE WHEN sqlc.arg(checked)::boolean THEN sqlc.arg(tick_at)::timestamptz END) ELSE checked_at END,
    tick_at = greatest(coalesce(tick_at, sqlc.arg(tick_at)::timestamptz), sqlc.arg(tick_at)::timestamptz),
    updated_at = now()
WHERE id = sqlc.arg(id) AND household_id = sqlc.arg(household_id)
RETURNING *;

-- name: DeleteListItem :execrows
DELETE FROM list_items WHERE id = $1 AND household_id = $2;

-- name: SetListItemHaveIt :one
-- Only Check at home items can be answered; NULL clears the answer.
UPDATE list_items SET have_it = sqlc.narg(have_it), updated_at = now()
WHERE id = sqlc.arg(id) AND household_id = sqlc.arg(household_id) AND check_home
RETURNING *;

-- name: AddRecipeListItem :one
-- An ingredient of a recipe added to this week: a manual item with its amount.
INSERT INTO list_items (household_id, plan_id, name, normalized_name, qty_text, section, is_manual, added_by)
VALUES ($1, $2, $3, $4, $5, $6, true, $7) RETURNING *;

-- name: ClearCheckedListItems :many
-- Items to buy that are in the basket. Check at home items count as to buy
-- only once answered "Buy" (have_it false), as in domain.ListItem.ToBuy.
UPDATE list_items SET cleared_at = now(), cleared_batch = sqlc.arg(batch), updated_at = now()
WHERE household_id = sqlc.arg(household_id) AND plan_id = sqlc.arg(plan_id)
  AND checked AND cleared_at IS NULL AND (NOT check_home OR have_it = false)
RETURNING id;

-- name: RestoreClearedListItems :execrows
UPDATE list_items SET cleared_at = NULL, cleared_batch = NULL, updated_at = now()
WHERE household_id = $1 AND cleared_batch = $2;
