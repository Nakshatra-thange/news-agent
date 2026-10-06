-- Record how many candidates a fetch run rejected during normalization
-- (missing title, unparseable URL, ...), so dropped input is visible rather
-- than silently lost. Additive and backwards compatible.

-- +goose Up
ALTER TABLE fetch_runs
    ADD COLUMN items_rejected integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT fetch_runs_rejected_nonneg CHECK (items_rejected >= 0);

-- +goose Down
ALTER TABLE fetch_runs DROP COLUMN items_rejected;
