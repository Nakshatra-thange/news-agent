-- AI enrichment: structured intelligence extracted from an item by an LLM.
-- Kept apart from `items` so enrichment never touches ingested data. One
-- row per item (item_id is the primary key); re-enriching replaces it.

-- +goose Up
CREATE TABLE item_enrichments (
    item_id        uuid        PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    -- Short lowercase subject labels, e.g. {"agents", "code generation"}.
    topics         text[]      NOT NULL,
    -- Named things the item is about: [{"name": "...", "type": "organization"}].
    entities       jsonb       NOT NULL,
    -- How much the item matters to an AI practitioner, 1 (minor) to 5 (major).
    importance     smallint    NOT NULL,
    -- Classification: what kind of development this is (research,
    -- model_release, tool, ...), validated against a fixed list in Go.
    category       text        NOT NULL,
    -- Provenance: who produced this and from which item content. An item
    -- whose content_hash, model or prompt version changed is stale.
    provider       text        NOT NULL,
    model          text        NOT NULL,
    prompt_version integer     NOT NULL,
    content_hash   bytea       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT item_enrichments_importance_range CHECK (importance BETWEEN 1 AND 5),
    CONSTRAINT item_enrichments_entities_array   CHECK (jsonb_typeof(entities) = 'array'),
    CONSTRAINT item_enrichments_category_format  CHECK (category ~ '^[a-z][a-z_]{0,31}$'),
    CONSTRAINT item_enrichments_provider_format  CHECK (provider ~ '^[a-z][a-z0-9_-]{0,31}$'),
    CONSTRAINT item_enrichments_model_not_blank  CHECK (btrim(model) <> ''),
    CONSTRAINT item_enrichments_prompt_version   CHECK (prompt_version > 0),
    CONSTRAINT item_enrichments_content_sha256   CHECK (octet_length(content_hash) = 32)
);

-- +goose Down
DROP TABLE item_enrichments;
