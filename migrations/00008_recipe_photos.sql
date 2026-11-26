-- +goose Up
-- Design step C: the family's own photo of a recipe replaces its dish-type
-- drawing. One photo per recipe, already resized to a JPEG (imagefit).
CREATE TABLE recipe_photos (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    recipe_id    uuid NOT NULL,
    photo        bytea NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (household_id, recipe_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE recipe_photos;
