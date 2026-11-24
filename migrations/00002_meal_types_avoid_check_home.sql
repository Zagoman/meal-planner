-- +goose Up
-- Window 1: meal types and targets, the avoid list as its own table, and
-- "Check at home" as a flag on list items instead of a section.

-- Profiles: meal counts per type replace dinners_per_week; servings becomes
-- people; the avoid array moves to avoid_items (below).
ALTER TABLE profiles ADD COLUMN meal_targets jsonb NOT NULL
    DEFAULT '{"breakfast": 0, "lunch": 0, "dinner": 4, "baking": 0}'::jsonb;
UPDATE profiles SET meal_targets = jsonb_build_object(
    'breakfast', 0, 'lunch', 0, 'dinner', dinners_per_week, 'baking', 0);
ALTER TABLE profiles DROP COLUMN dinners_per_week;
ALTER TABLE profiles RENAME COLUMN servings TO people;
ALTER TABLE profiles ADD COLUMN auto_confirm_time time NOT NULL DEFAULT '08:00';

CREATE TABLE avoid_items (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    normalized_name text NOT NULL CHECK (normalized_name <> ''),
    added_by        uuid REFERENCES members (id) ON DELETE SET NULL,
    added_at        timestamptz NOT NULL DEFAULT now(),
    source          text NOT NULL DEFAULT 'profile' CHECK (source IN ('setup', 'recipe', 'profile')),
    UNIQUE (household_id, normalized_name)
);

-- Copy the old array. The normalized name here is a close SQL approximation
-- of shopping.Normalize (lowercase, cut at ',' or '(', non-alphanumerics to
-- spaces, collapsed); the app re-normalizes whenever the list is saved.
INSERT INTO avoid_items (household_id, name, normalized_name, source)
SELECT p.household_id, a.name, a.norm, 'profile'
FROM profiles p
CROSS JOIN LATERAL (
    SELECT DISTINCT ON (norm) left(btrim(x), 100) AS name, norm
    FROM unnest(p.avoid) AS x,
         LATERAL (SELECT btrim(regexp_replace(
                      regexp_replace(lower(split_part(split_part(x, ',', 1), '(', 1)), '[^[:alnum:]]+', ' ', 'g'),
                      '\s+', ' ', 'g')) AS norm) n
    WHERE norm <> ''
    ORDER BY norm, x
) a
ON CONFLICT DO NOTHING;
ALTER TABLE profiles DROP COLUMN avoid;

-- Meals: a type (breakfast, lunch, dinner, baking, other) and an optional
-- suggested day. A plan is a set of meals, so a day can hold several.
ALTER TABLE meals DROP CONSTRAINT meals_plan_id_day_key;
ALTER TABLE meals ALTER COLUMN day DROP NOT NULL;
ALTER TABLE meals ADD COLUMN meal_type text NOT NULL DEFAULT 'dinner'
    CHECK (meal_type IN ('breakfast', 'lunch', 'dinner', 'baking', 'other'));
ALTER TABLE meals ADD COLUMN position integer NOT NULL DEFAULT 0;
ALTER TABLE meals ADD COLUMN recipe_id uuid;
ALTER TABLE meals ADD COLUMN added_from text NOT NULL DEFAULT 'claude'
    CHECK (added_from IN ('claude', 'library', 'not_cooked'));
ALTER TABLE meals ADD FOREIGN KEY (household_id, recipe_id)
    REFERENCES recipes (household_id, id) ON DELETE SET NULL (recipe_id);
CREATE INDEX meals_plan_idx ON meals (household_id, plan_id);

-- List items: "Check at home" is a flag, so the item keeps its real section
-- and "Buy" can move it there.
ALTER TABLE list_items ADD COLUMN check_home boolean NOT NULL DEFAULT false;
ALTER TABLE list_items ADD COLUMN have_it boolean;
ALTER TABLE list_items ADD COLUMN added_by uuid REFERENCES members (id) ON DELETE SET NULL;
UPDATE list_items li SET
    check_home = true,
    section = coalesce((
        SELECT i.section FROM ingredients i
        JOIN meals m ON m.id = i.meal_id AND m.household_id = i.household_id
        WHERE m.plan_id = li.plan_id AND i.household_id = li.household_id
          AND i.normalized_name = li.normalized_name
        LIMIT 1), 'other')
WHERE li.section = 'pantry_check';

-- +goose Down
UPDATE list_items SET section = 'pantry_check' WHERE check_home;
ALTER TABLE list_items DROP COLUMN added_by, DROP COLUMN have_it, DROP COLUMN check_home;

DROP INDEX meals_plan_idx;
ALTER TABLE meals DROP COLUMN added_from, DROP COLUMN recipe_id, DROP COLUMN position, DROP COLUMN meal_type;
DELETE FROM meals WHERE day IS NULL;
ALTER TABLE meals ALTER COLUMN day SET NOT NULL;
-- Plans with more than one meal on a day cannot go back; keep the first.
DELETE FROM meals m USING meals o
WHERE m.plan_id = o.plan_id AND m.day = o.day AND m.created_at > o.created_at;
ALTER TABLE meals ADD CONSTRAINT meals_plan_id_day_key UNIQUE (plan_id, day);

ALTER TABLE profiles ADD COLUMN avoid text[] NOT NULL DEFAULT '{}';
UPDATE profiles p SET avoid = coalesce(
    (SELECT array_agg(name ORDER BY added_at) FROM avoid_items a WHERE a.household_id = p.household_id), '{}');
DROP TABLE avoid_items;

ALTER TABLE profiles DROP COLUMN auto_confirm_time;
ALTER TABLE profiles RENAME COLUMN people TO servings;
ALTER TABLE profiles ADD COLUMN dinners_per_week integer NOT NULL DEFAULT 4 CHECK (dinners_per_week BETWEEN 1 AND 7);
UPDATE profiles SET dinners_per_week = least(greatest(coalesce((meal_targets ->> 'dinner')::int, 4), 1), 7);
ALTER TABLE profiles DROP COLUMN meal_targets;
