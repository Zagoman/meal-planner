-- +goose Up
-- Window 4: email sign-in links, joining from an invite, and the four setup
-- questions for a new household.

-- A sign-in link: single use, 15 minutes, only the hash stored. A link sent
-- from an invite (invite_id) adds the membership when it is used, so typing
-- someone else's email on an invite page never adds them to a household.
CREATE TABLE login_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    member_id    uuid NOT NULL REFERENCES members (id) ON DELETE CASCADE,
    invite_id    uuid REFERENCES invites (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_tokens_member_idx ON login_tokens (member_id, created_at DESC);

-- New households answer the four setup questions once; existing ones have.
ALTER TABLE profiles ADD COLUMN setup_done boolean NOT NULL DEFAULT false;
UPDATE profiles SET setup_done = true;

-- +goose Down
ALTER TABLE profiles DROP COLUMN setup_done;
DROP TABLE login_tokens;
