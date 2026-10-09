-- Item embeddings: one vector per item and embedding model, used later to
-- compare items (story clustering). Kept apart from `items` so embedding
-- never touches ingested data.
--
-- Vectors are stored as real[] (float4) rather than pgvector: the local
-- PostgreSQL install does not ship the extension, and similarity is
-- computed in Go over a bounded set of items, so no vector index is needed.

-- +goose Up
CREATE TABLE item_embeddings (
    item_id      uuid        NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    model        text        NOT NULL,
    dimensions   integer     NOT NULL,
    embedding    real[]      NOT NULL,
    -- The item's content_hash (title + description) when it was embedded.
    -- An item whose content changed is due a new embedding.
    content_hash bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    -- At most one embedding per item and model; re-embedding replaces it.
    PRIMARY KEY (item_id, model),

    CONSTRAINT item_embeddings_dimensions      CHECK (dimensions BETWEEN 1 AND 4096),
    CONSTRAINT item_embeddings_length          CHECK (array_ndims(embedding) = 1
                                                      AND coalesce(array_length(embedding, 1), 0) = dimensions),
    CONSTRAINT item_embeddings_finite          CHECK (array_position(embedding, NULL) IS NULL
                                                      AND NOT embedding && ARRAY['NaN', 'Infinity', '-Infinity']::real[]),
    CONSTRAINT item_embeddings_provider_format CHECK (provider ~ '^[a-z][a-z0-9_-]{0,31}$'),
    CONSTRAINT item_embeddings_model_not_blank CHECK (btrim(model) <> ''),
    CONSTRAINT item_embeddings_content_sha256  CHECK (octet_length(content_hash) = 32)
);

-- +goose Down
DROP TABLE item_embeddings;
