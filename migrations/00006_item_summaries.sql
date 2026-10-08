-- LLM-generated summaries: a short neutral paragraph per item. Kept apart
-- from `items` (like item_enrichments) so summarizing never touches
-- ingested data. One row per item; re-summarizing replaces it.

-- +goose Up
CREATE TABLE item_summaries (
    item_id        uuid        PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    summary        text        NOT NULL,
    -- Provenance: who wrote it and from which item content. An item whose
    -- content_hash, model or prompt version changed is due a new summary.
    provider       text        NOT NULL,
    model          text        NOT NULL,
    prompt_version integer     NOT NULL,
    content_hash   bytea       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT item_summaries_length          CHECK (char_length(summary) BETWEEN 40 AND 600),
    CONSTRAINT item_summaries_single_line     CHECK (position(E'\n' in summary) = 0),
    CONSTRAINT item_summaries_provider_format CHECK (provider ~ '^[a-z][a-z0-9_-]{0,31}$'),
    CONSTRAINT item_summaries_model_not_blank CHECK (btrim(model) <> ''),
    CONSTRAINT item_summaries_prompt_version  CHECK (prompt_version > 0),
    CONSTRAINT item_summaries_content_sha256  CHECK (octet_length(content_hash) = 32)
);

-- +goose Down
DROP TABLE item_summaries;
