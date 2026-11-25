-- +goose Up
-- Window 5: recipes imported from a photo, text or link wait as drafts until
-- someone checks and saves them; offline ticks apply in tick order.

CREATE TABLE recipe_drafts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    source       text NOT NULL CHECK (source IN ('photo', 'text', 'link')),
    -- The pasted text or the link. A photo is kept in photo only until it
    -- has been read, then cleared.
    raw_input    text NOT NULL DEFAULT '',
    photo        bytea,
    photo_type   text NOT NULL DEFAULT '',
    parsed       jsonb,
    -- Field paths Claude could not read clearly ("ingredients[2].qty").
    uncertain    text[] NOT NULL DEFAULT '{}',
    created_by   uuid REFERENCES members (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (household_id, id)
);
CREATE INDEX recipe_drafts_household_idx ON recipe_drafts (household_id, created_at DESC);

-- When the tick that set `checked` was made (on the phone, for an offline
-- tick). A tick older than the one applied is ignored: the last tick wins.
ALTER TABLE list_items ADD COLUMN tick_at timestamptz;

-- +goose Down
ALTER TABLE list_items DROP COLUMN tick_at;
DROP TABLE recipe_drafts;
