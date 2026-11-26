-- +goose Up
-- Only recipes the household cooked, saved or added itself are in the
-- library (/recipes). Every planned meal still links to a recipe, so past
-- plans keep opening; recipes outside the library are never deleted.
ALTER TABLE recipes ADD COLUMN in_library boolean NOT NULL DEFAULT false;

UPDATE recipes r SET in_library = true
WHERE r.source <> 'claude'
   OR r.parent_recipe_id IS NOT NULL
   OR EXISTS (SELECT 1 FROM meals m
              JOIN meal_outcomes o ON o.meal_id = m.id AND o.household_id = m.household_id
              WHERE m.recipe_id = r.id AND m.household_id = r.household_id AND o.status = 'cooked');

CREATE INDEX recipes_library_idx ON recipes (household_id, meal_type, title_key) WHERE in_library;

-- +goose Down
DROP INDEX recipes_library_idx;
ALTER TABLE recipes DROP COLUMN in_library;
