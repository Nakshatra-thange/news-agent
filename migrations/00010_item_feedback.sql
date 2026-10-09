-- Explicit feedback of the (single, local) user on items: "more like this"
-- or "less like this". One row per item: giving feedback again replaces it,
-- and removing it deletes the row. Absence means no opinion.

-- +goose Up
CREATE TABLE item_feedback (
    item_id    uuid        PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    value      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT item_feedback_value CHECK (value IN ('more', 'less'))
);

-- Signals are built from the most recent feedback.
CREATE INDEX item_feedback_updated_at ON item_feedback (updated_at DESC, item_id);

-- +goose Down
DROP TABLE item_feedback;
