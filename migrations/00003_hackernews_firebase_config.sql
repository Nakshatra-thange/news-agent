-- The Hacker News adapter uses the official Firebase API (ranked story
-- lists + per-item requests) instead of a search API. Its configuration
-- gains "lists" and "max_items" and loses "max_results_per_query", which
-- strict config validation would otherwise reject on existing sources.
-- Explicit existing values win over the new defaults.

-- +goose Up
UPDATE sources
SET config = '{"lists": ["top", "best"], "max_items": 300}'::jsonb || (config - 'max_results_per_query'),
    updated_at = now()
WHERE type = 'hackernews';

-- +goose Down
UPDATE sources
SET config = (config - 'lists' - 'max_items') || '{"max_results_per_query": 50}'::jsonb,
    updated_at = now()
WHERE type = 'hackernews';
