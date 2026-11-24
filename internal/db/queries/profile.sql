-- name: CreateProfile :exec
INSERT INTO profiles (household_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: GetProfile :one
SELECT * FROM profiles WHERE household_id = $1;

-- name: UpdateProfile :one
UPDATE profiles SET
    meal_targets      = $2,
    people            = $3,
    spice_level       = $4,
    one_pot           = $5,
    protein_mix       = $6,
    shop              = $7,
    shopping_day      = $8,
    baby_portions     = $9,
    language          = $10,
    auto_confirm_time = $11,
    updated_at        = now()
WHERE household_id = $1
RETURNING *;

-- name: ListSections :many
SELECT * FROM store_sections WHERE household_id = $1 ORDER BY sort_order, name;

-- name: DeleteSections :exec
DELETE FROM store_sections WHERE household_id = $1;

-- name: InsertSection :exec
INSERT INTO store_sections (household_id, key, name, sort_order) VALUES ($1, $2, $3, $4);

-- name: ListPantryItems :many
SELECT * FROM pantry_items WHERE household_id = $1 ORDER BY name;

-- name: DeletePantryItems :exec
DELETE FROM pantry_items WHERE household_id = $1;

-- name: InsertPantryItem :exec
INSERT INTO pantry_items (household_id, name, normalized_name) VALUES ($1, $2, $3)
ON CONFLICT (household_id, normalized_name) DO NOTHING;

-- name: ListAvoidItems :many
SELECT * FROM avoid_items WHERE household_id = $1 ORDER BY lower(name);

-- name: InsertAvoidItem :one
-- Adding a name that is already on the list returns nothing (pgx.ErrNoRows).
INSERT INTO avoid_items (household_id, name, normalized_name, added_by, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (household_id, normalized_name) DO NOTHING
RETURNING *;

-- name: DeleteAvoidItem :execrows
DELETE FROM avoid_items WHERE id = $1 AND household_id = $2;

-- name: DeleteAvoidItemsNotIn :exec
DELETE FROM avoid_items
WHERE household_id = $1 AND NOT (normalized_name = ANY(sqlc.arg(keep)::text[]));
