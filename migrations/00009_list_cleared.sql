-- +goose Up
-- Shopping list fixes #6: "Clear ticked" / "Done shopping" hide the items
-- already in the basket. A soft delete, so Undo can bring back exactly the
-- items one clear took (cleared_batch), and a rebuild keeps them hidden.
ALTER TABLE list_items ADD COLUMN cleared_at timestamptz;
ALTER TABLE list_items ADD COLUMN cleared_batch uuid;
CREATE INDEX list_items_cleared_batch ON list_items (household_id, cleared_batch) WHERE cleared_batch IS NOT NULL;

-- +goose Down
DROP INDEX list_items_cleared_batch;
ALTER TABLE list_items DROP COLUMN cleared_batch;
ALTER TABLE list_items DROP COLUMN cleared_at;
