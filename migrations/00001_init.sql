-- +goose Up
-- Household isolation is enforced twice: every query filters by household_id,
-- and every child row references its parent through a composite
-- (household_id, id) foreign key, so a row can never point at another
-- household's data even if a query forgets the filter.

CREATE TABLE households (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE members (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text NOT NULL,
    name            text NOT NULL DEFAULT '',
    password_hash   text NOT NULL DEFAULT '',
    is_server_admin boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX members_email_key ON members (lower(email));

CREATE TABLE household_members (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    member_id    uuid NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('admin', 'member')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (household_id, member_id)
);
CREATE INDEX household_members_member_idx ON household_members (member_id);

CREATE TABLE invites (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    role         text NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
    created_by   uuid REFERENCES members (id) ON DELETE SET NULL,
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    accepted_by  uuid REFERENCES members (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX invites_household_idx ON invites (household_id);

CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL,
    member_id    uuid NOT NULL,
    name         text NOT NULL DEFAULT '',
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz,
    FOREIGN KEY (household_id, member_id)
        REFERENCES household_members (household_id, member_id) ON DELETE CASCADE
);

CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL,
    member_id    uuid NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, member_id)
        REFERENCES household_members (household_id, member_id) ON DELETE CASCADE
);
CREATE INDEX sessions_member_idx ON sessions (member_id);

CREATE TABLE profiles (
    household_id     uuid PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    dinners_per_week integer NOT NULL DEFAULT 4 CHECK (dinners_per_week BETWEEN 1 AND 7),
    servings         integer NOT NULL DEFAULT 2 CHECK (servings BETWEEN 1 AND 12),
    spice_level      text    NOT NULL DEFAULT 'mild' CHECK (spice_level IN ('none', 'mild', 'medium', 'hot')),
    one_pot          boolean NOT NULL DEFAULT false,
    -- {"chicken": 2, "fish": 1, "vegetarian": 1, "other": 0}: dinners per week by protein
    protein_mix      jsonb   NOT NULL DEFAULT '{}'::jsonb,
    avoid            text[]  NOT NULL DEFAULT '{}',
    shop             text    NOT NULL DEFAULT '',
    shopping_day     text    NOT NULL DEFAULT 'saturday' CHECK (shopping_day IN
                         ('monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday')),
    baby_portions    boolean NOT NULL DEFAULT false,
    language         text    NOT NULL DEFAULT 'en',
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE store_sections (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    key          text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'),
    name         text NOT NULL,
    sort_order   integer NOT NULL,
    UNIQUE (household_id, key)
);

CREATE TABLE pantry_items (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    name            text NOT NULL,
    normalized_name text NOT NULL,
    UNIQUE (household_id, normalized_name)
);

CREATE TABLE plans (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    week_start   date NOT NULL,
    -- draft: being generated/validated; active: the current plan; archived: replaced
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    prompt       text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES members (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (household_id, id)
);
CREATE UNIQUE INDEX plans_one_active ON plans (household_id) WHERE status = 'active';

CREATE TABLE meals (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id       uuid NOT NULL,
    plan_id            uuid NOT NULL,
    day                text NOT NULL CHECK (day IN
                           ('monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday')),
    title              text NOT NULL,
    protein            text NOT NULL CHECK (protein IN ('chicken', 'fish', 'vegetarian', 'other')),
    minutes            integer NOT NULL CHECK (minutes > 0),
    one_pot            boolean NOT NULL,
    servings           integer NOT NULL CHECK (servings > 0),
    after_baby_portion text[] NOT NULL DEFAULT '{}',
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (household_id, id),
    UNIQUE (plan_id, day),
    FOREIGN KEY (household_id, plan_id) REFERENCES plans (household_id, id) ON DELETE CASCADE
);

CREATE TABLE ingredients (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    uuid NOT NULL,
    meal_id         uuid NOT NULL,
    position        integer NOT NULL,
    name            text NOT NULL,
    normalized_name text NOT NULL,
    qty             numeric(10, 3) NOT NULL CHECK (qty >= 0),
    unit            text NOT NULL,
    section         text NOT NULL,
    FOREIGN KEY (household_id, meal_id) REFERENCES meals (household_id, id) ON DELETE CASCADE
);
CREATE INDEX ingredients_meal_idx ON ingredients (meal_id);

CREATE TABLE steps (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL,
    meal_id      uuid NOT NULL,
    position     integer NOT NULL,
    text         text NOT NULL,
    timer_min    integer CHECK (timer_min > 0),
    baby_portion boolean NOT NULL DEFAULT false,
    FOREIGN KEY (household_id, meal_id) REFERENCES meals (household_id, id) ON DELETE CASCADE
);
CREATE INDEX steps_meal_idx ON steps (meal_id);

CREATE TABLE list_items (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id    uuid NOT NULL,
    plan_id         uuid NOT NULL,
    name            text NOT NULL,
    normalized_name text NOT NULL,
    qty_text        text NOT NULL DEFAULT '',   -- buying unit, e.g. "1 net"
    need_text       text NOT NULL DEFAULT '',   -- amount the recipes need, e.g. "need 3"
    section         text NOT NULL,              -- store_sections.key, or 'pantry_check'
    checked         boolean NOT NULL DEFAULT false,
    checked_by      uuid REFERENCES members (id) ON DELETE SET NULL,
    checked_at      timestamptz,
    is_manual       boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (household_id, plan_id) REFERENCES plans (household_id, id) ON DELETE CASCADE
);
CREATE INDEX list_items_plan_idx ON list_items (household_id, plan_id);
-- Generated items are keyed by normalized_name so a rebuild can keep ticks.
CREATE UNIQUE INDEX list_items_generated_key ON list_items (plan_id, normalized_name) WHERE NOT is_manual;

CREATE TABLE recipes (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id   uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    source_meal_id uuid,
    title          text NOT NULL,
    protein        text NOT NULL,
    minutes        integer NOT NULL,
    one_pot        boolean NOT NULL,
    servings       integer NOT NULL,
    -- the meal in the meal JSON schema shape (ingredients, steps, after_baby_portion)
    data           jsonb NOT NULL,
    created_by     uuid REFERENCES members (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (household_id, id),
    FOREIGN KEY (household_id, source_meal_id) REFERENCES meals (household_id, id) ON DELETE SET NULL (source_meal_id)
);

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

CREATE TABLE usage_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    household_id  uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    member_id     uuid REFERENCES members (id) ON DELETE SET NULL,
    kind          text NOT NULL CHECK (kind IN ('plan', 'swap', 'retry')),
    model         text NOT NULL,
    input_tokens  integer NOT NULL,
    output_tokens integer NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_log_household_time_idx ON usage_log (household_id, created_at);

-- +goose Down
DROP TABLE usage_log, feedback, recipes, list_items, steps, ingredients, meals, plans,
    pantry_items, store_sections, profiles, sessions, api_tokens, invites,
    household_members, members, households;
