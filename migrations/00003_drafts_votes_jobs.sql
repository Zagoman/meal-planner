-- +goose Up
-- Window 2: a plan is made as a draft, reviewed (votes, swaps, Ask Claude)
-- and confirmed, by a member or automatically. Claude calls run as jobs.

-- Plans: "active" is the confirmed plan. At most one draft per household
-- and week, so a job that runs twice cannot make a second draft.
ALTER TABLE plans ADD COLUMN confirmed_by uuid REFERENCES members (id) ON DELETE SET NULL;
ALTER TABLE plans ADD COLUMN confirmed_at timestamptz;
ALTER TABLE plans ADD COLUMN auto_confirmed boolean NOT NULL DEFAULT false;
-- Shown on the Week page after an auto-confirm that kept disliked meals.
ALTER TABLE plans ADD COLUMN confirm_note text NOT NULL DEFAULT '';
UPDATE plans SET confirmed_at = created_at WHERE status IN ('active', 'archived');
-- Until now a draft only existed for the seconds a plan was being saved; any
-- left over is from a failed save.
DELETE FROM plans WHERE status = 'draft';
CREATE UNIQUE INDEX plans_one_draft_per_week ON plans (household_id, week_start) WHERE status = 'draft';

-- Schedules run in the household's time zone.
ALTER TABLE profiles ADD COLUMN time_zone text NOT NULL DEFAULT 'Europe/Copenhagen';

CREATE TABLE meal_votes (
    household_id uuid NOT NULL,
    meal_id      uuid NOT NULL,
    member_id    uuid NOT NULL,
    vote         smallint NOT NULL CHECK (vote IN (-1, 1)),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (meal_id, member_id),
    FOREIGN KEY (household_id, meal_id) REFERENCES meals (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, member_id) REFERENCES household_members (household_id, member_id) ON DELETE CASCADE
);
CREATE INDEX meal_votes_household_idx ON meal_votes (household_id);

CREATE TABLE jobs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('draft', 'swap', 'ask', 'auto_confirm', 'tweak', 'import')),
    status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- Scheduled jobs carry a key ("draft:2026-11-29"); a second job with the
    -- same key for the household is refused, so a schedule fires once.
    dedupe_key  text,
    plan_id     uuid,
    meal_id     uuid,
    member_id   uuid REFERENCES members (id) ON DELETE SET NULL,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
    result      jsonb,
    error       text NOT NULL DEFAULT '',
    attempts    integer NOT NULL DEFAULT 0,
    run_after   timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz,
    UNIQUE (household_id, dedupe_key)
);
CREATE INDEX jobs_queue_idx ON jobs (run_after, created_at) WHERE status = 'queued';
CREATE INDEX jobs_household_idx ON jobs (household_id, kind, created_at DESC);
CREATE INDEX jobs_meal_idx ON jobs (household_id, meal_id) WHERE meal_id IS NOT NULL;

ALTER TABLE usage_log DROP CONSTRAINT usage_log_kind_check;
ALTER TABLE usage_log ADD CONSTRAINT usage_log_kind_check
    CHECK (kind IN ('plan', 'swap', 'retry', 'ask', 'auto_confirm', 'tweak', 'import'));

-- +goose Down
DELETE FROM usage_log WHERE kind NOT IN ('plan', 'swap', 'retry');
ALTER TABLE usage_log DROP CONSTRAINT usage_log_kind_check;
ALTER TABLE usage_log ADD CONSTRAINT usage_log_kind_check CHECK (kind IN ('plan', 'swap', 'retry'));
DROP TABLE jobs;
DROP TABLE meal_votes;
ALTER TABLE profiles DROP COLUMN time_zone;
DROP INDEX plans_one_draft_per_week;
DELETE FROM plans WHERE status = 'draft';
ALTER TABLE plans DROP COLUMN confirm_note, DROP COLUMN auto_confirmed, DROP COLUMN confirmed_at, DROP COLUMN confirmed_by;
