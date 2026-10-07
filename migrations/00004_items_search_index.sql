-- Full-text search for the feed's q parameter. An expression index (no new
-- column): the feed query must use exactly this expression to match it.

-- +goose Up
CREATE INDEX items_search_idx ON items
    USING gin (to_tsvector('english'::regconfig, title || ' ' || description));

-- +goose Down
DROP INDEX items_search_idx;
