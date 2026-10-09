-- Explicit feed preferences of the (single, local) user: topics and sources
-- of interest. They boost matching items in the personalized ranked feed.
-- There are no users yet, so each table is the whole preference set; a
-- user_id column comes with authentication.

-- +goose Up
CREATE TABLE preferred_topics (
    -- Stored trimmed and lowercase; matched case-insensitively against item
    -- tags and enrichment topics.
    topic      text        PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT preferred_topics_format CHECK (topic = lower(btrim(topic)) AND char_length(topic) BETWEEN 1 AND 100)
);

CREATE TABLE preferred_sources (
    source_id  uuid        PRIMARY KEY REFERENCES sources (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE preferred_sources;
DROP TABLE preferred_topics;
