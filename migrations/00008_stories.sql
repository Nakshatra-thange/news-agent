-- Stories: groups of items about the same underlying development, built
-- from item embeddings by `synergy cluster`. Kept apart from `items`.
--
-- Stories are per embedding model: vectors from different models are not
-- comparable, so an item may belong to one story per model.

-- +goose Up
CREATE TABLE stories (
    id           uuid        PRIMARY KEY,
    -- The embedding model whose vectors built this story.
    model        text        NOT NULL,
    -- The first item of the story; new items are compared with its vector.
    seed_item_id uuid        NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),

    UNIQUE (id, model),
    UNIQUE (seed_item_id, model),
    CONSTRAINT stories_model_not_blank CHECK (btrim(model) <> '')
);

CREATE TABLE story_items (
    story_id   uuid        NOT NULL,
    item_id    uuid        NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    model      text        NOT NULL,
    -- Cosine similarity to the story's seed when the item joined (1 for
    -- the seed itself).
    similarity real        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- At most one story per item and model.
    PRIMARY KEY (item_id, model),
    -- The membership's model must be its story's model.
    FOREIGN KEY (story_id, model) REFERENCES stories (id, model) ON DELETE CASCADE,
    CONSTRAINT story_items_similarity CHECK (similarity BETWEEN -1 AND 1)
);

CREATE INDEX story_items_story_id ON story_items (story_id);

-- +goose Down
DROP TABLE story_items;
DROP TABLE stories;
