-- +goose Up
-- Design step B: the setup's optional "Any cuisines you love?" answer, used
-- in the planning prompt. Values come from domain.Cuisines.
ALTER TABLE profiles ADD COLUMN cuisines text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE profiles DROP COLUMN cuisines;
