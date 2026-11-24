-- +goose Up
-- Window 3: what happened to each meal (cooked with a rating, or not
-- cooked) and the recipe library every planned meal is saved to.

-- One outcome per meal for the household; whoever set it last.
CREATE TABLE meal_outcomes (
    household_id uuid NOT NULL,
    meal_id      uuid NOT NULL PRIMARY KEY,
    status       text NOT NULL CHECK (status IN ('cooked', 'not_cooked')),
    rating       integer CHECK (rating BETWEEN 1 AND 5),
    note         text NOT NULL DEFAULT '',
    member_id    uuid REFERENCES members (id) ON DELETE SET NULL,
    at           timestamptz NOT NULL DEFAULT now(),
    CHECK (status = 'cooked' OR rating IS NULL),
    FOREIGN KEY (household_id, meal_id) REFERENCES meals (household_id, id) ON DELETE CASCADE
);
CREATE INDEX meal_outcomes_household_time_idx ON meal_outcomes (household_id, at);

-- feedback had one rating per member; keep the latest per meal.
INSERT INTO meal_outcomes (household_id, meal_id, status, rating, note, member_id, at)
SELECT DISTINCT ON (meal_id) household_id, meal_id, 'cooked', rating, note, member_id, created_at
FROM feedback ORDER BY meal_id, created_at DESC;
DROP TABLE feedback;

-- Recipes: where they came from, tweaked versions, meal type, and a title
-- key so a meal Claude suggests again reuses its recipe.
ALTER TABLE recipes ADD COLUMN source text NOT NULL DEFAULT 'claude'
    CHECK (source IN ('claude', 'photo', 'text', 'link', 'manual'));
ALTER TABLE recipes ADD COLUMN parent_recipe_id uuid;
ALTER TABLE recipes ADD FOREIGN KEY (household_id, parent_recipe_id)
    REFERENCES recipes (household_id, id) ON DELETE SET NULL (parent_recipe_id);
ALTER TABLE recipes ADD COLUMN meal_type text NOT NULL DEFAULT 'dinner'
    CHECK (meal_type IN ('breakfast', 'lunch', 'dinner', 'baking', 'other'));
ALTER TABLE recipes ADD COLUMN title_key text NOT NULL DEFAULT '';
UPDATE recipes SET
    meal_type = coalesce(nullif(data ->> 'meal_type', ''), 'dinner'),
    title_key = lower(regexp_replace(btrim(title), '\s+', ' ', 'g'));
-- A household's own saved copies may repeat a title; Claude's originals not.
DELETE FROM recipes r USING recipes o
WHERE r.household_id = o.household_id AND r.meal_type = o.meal_type AND r.title_key = o.title_key
  AND r.created_at > o.created_at;
CREATE UNIQUE INDEX recipes_claude_title ON recipes (household_id, meal_type, title_key)
    WHERE source = 'claude' AND parent_recipe_id IS NULL;
CREATE INDEX recipes_household_idx ON recipes (household_id, created_at DESC);

-- Every existing meal becomes (or joins) a library recipe.
INSERT INTO recipes (household_id, source_meal_id, title, protein, minutes, one_pot, servings, data,
                     created_by, created_at, source, meal_type, title_key)
SELECT DISTINCT ON (m.household_id, m.meal_type, lower(regexp_replace(btrim(m.title), '\s+', ' ', 'g')))
    m.household_id, m.id, m.title, m.protein, m.minutes, m.one_pot, m.servings,
    jsonb_build_object(
        'meal_type', m.meal_type, 'day', NULL, 'title', m.title, 'protein', m.protein,
        'minutes', m.minutes, 'one_pot', m.one_pot, 'servings', m.servings,
        'after_baby_portion', to_jsonb(m.after_baby_portion),
        'ingredients', coalesce((
            SELECT jsonb_agg(jsonb_build_object('name', i.name, 'normalized_name', i.normalized_name,
                'qty', i.qty, 'unit', i.unit, 'section', i.section) ORDER BY i.position)
            FROM ingredients i WHERE i.meal_id = m.id AND i.household_id = m.household_id), '[]'::jsonb),
        'steps', coalesce((
            SELECT jsonb_agg(jsonb_build_object('text', s.text, 'timer_min', s.timer_min,
                'baby_portion', s.baby_portion) ORDER BY s.position)
            FROM steps s WHERE s.meal_id = m.id AND s.household_id = m.household_id), '[]'::jsonb)),
    p.created_by, m.created_at, 'claude', m.meal_type,
    lower(regexp_replace(btrim(m.title), '\s+', ' ', 'g'))
FROM meals m JOIN plans p ON p.id = m.plan_id AND p.household_id = m.household_id
ORDER BY m.household_id, m.meal_type, lower(regexp_replace(btrim(m.title), '\s+', ' ', 'g')), m.created_at DESC
ON CONFLICT DO NOTHING;
UPDATE meals m SET recipe_id = r.id
FROM recipes r
WHERE m.recipe_id IS NULL AND r.household_id = m.household_id AND r.meal_type = m.meal_type
  AND r.title_key = lower(regexp_replace(btrim(m.title), '\s+', ' ', 'g'))
  AND r.source = 'claude' AND r.parent_recipe_id IS NULL;

-- +goose Down
UPDATE meals SET recipe_id = NULL;
DELETE FROM recipes WHERE source_meal_id IS NOT NULL AND source = 'claude';
DROP INDEX recipes_household_idx;
DROP INDEX recipes_claude_title;
ALTER TABLE recipes DROP COLUMN title_key, DROP COLUMN meal_type, DROP COLUMN parent_recipe_id, DROP COLUMN source;
CREATE TABLE feedback (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL,
    meal_id      uuid NOT NULL,
    member_id    uuid REFERENCES members (id) ON DELETE SET NULL,
    rating       integer NOT NULL CHECK (rating BETWEEN 1 AND 5),
    note         text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (meal_id, member_id),
    FOREIGN KEY (household_id, meal_id) REFERENCES meals (household_id, id) ON DELETE CASCADE
);
CREATE INDEX feedback_household_time_idx ON feedback (household_id, created_at);
INSERT INTO feedback (household_id, meal_id, member_id, rating, note, created_at)
SELECT household_id, meal_id, member_id, rating, note, at FROM meal_outcomes WHERE rating IS NOT NULL;
DROP TABLE meal_outcomes;
