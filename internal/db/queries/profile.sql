-- name: CreateProfile :exec
INSERT INTO profiles (household_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: GetProfile :one
SELECT * FROM profiles WHERE household_id = $1;

-- name: UpdateProfile :one
UPDATE profiles SET
    dinners_per_week = $2,
    servings         = $3,
    spice_level      = $4,
    one_pot          = $5,
    protein_mix      = $6,
    avoid            = $7,
    shop             = $8,
    shopping_day     = $9,
    baby_portions    = $10,
    language         = $11,
    updated_at       = now()
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
