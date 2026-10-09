# Synergy roadmap

## Phase 1: core ingestion and feed infrastructure (complete)

The goal of Phase 1 was a reliable foundation: get real AI-development
content from a few high-signal sources into PostgreSQL, cleanly and
reproducibly, and serve it as a feed. It was delivered in seven reviewed
stages:

| Stage | Delivered |
|---|---|
| 1. Skeleton | Go module, single binary with subcommands, validated env configuration, structured logging, HTTP server with request IDs, panic recovery, JSON errors, graceful shutdown, `/health` |
| 2. Database | Local PostgreSQL bootstrap (`make db-setup`), pgx pool, embedded goose migrations with advisory locking, core schema (sources, items, fetch runs), `/health/db`, isolated per-test schemas |
| 3. Source registry | Source types with strict per-type config, politeness floors, status lifecycle (active/paused/retired), priorities, seeding, source management REST API |
| 4. Ingestion core | Source-agnostic pipeline: normalization, URL canonicalization, same- and cross-source dedup, fetch runs, source health, rate-limited and retrying HTTP client, CLI and async API fetches |
| 5. Real sources | Hacker News (Firebase API), arXiv (Atom API), GitHub (search API, optional token) adapters, fixture-tested, with live smoke tests |
| 6. Feed API | `GET /api/v1/items` with keyset pagination, filters (source, type, status, kind, tags, time ranges), full-text search, duplicate handling; `GET /api/v1/items/{id}` |
| 7. Hardening | Full review, database outages reported as 503, abandoned-run recovery in the CLI and in source health, non-loopback bind warning, migration upgrade and concurrency tests, documentation |

Phase 1 explicitly does **not** include AI, ranking, personalization, users,
authentication, a frontend, a scheduler or sources beyond the three above.
The README's "Current limitations" section lists what that means in
practice.

## Phase 2.1: web app (complete)

A small Next.js app in `web/` that makes the Phase 1 feed usable:

- the feed with search, source tabs, source, kind and time pickers, tag
  filters and cursor-based "Load more"
- item pages with every stored detail and links to the original
- loading, empty, error and not-found states
- system-aware dark mode with a toggle, and a mobile layout

It uses only the existing API and needed no backend changes. The only
repository change outside `web/` keeps Go tooling out of `web/node_modules`.

Frontend ideas deliberately left for later:

- **Tag discovery.** The API has no tag or facet listing, so tags are found
  only on items. A `GET /api/v1/tags` (or facet counts on `/items`) would
  allow a tag picker.
- **Kind list.** The item kinds offered in the picker are listed in the
  frontend, because the API has no endpoint for them. A kinds list from the
  API would remove that duplication when a source type adds a kind.
- **Custom date ranges.** The time picker offers fixed windows; the API
  already accepts any `since`/`until`.
- **Source management and fetch triggering in the UI** (the API supports
  both), once authentication exists.
- **Frontend tests.** Verification was done with lint, typecheck, the
  production build and scripted headless-browser checks. A committed
  end-to-end suite (for example Playwright) can come with a CI setup.

## Phase 2.2: scheduler (complete)

`synergy serve` now fetches active sources automatically:

- Every `SCHEDULER_INTERVAL` (default 1m), it starts a fetch for each
  active source whose `min_fetch_interval` has elapsed since its last
  completed run (succeeded, failed or recovered as abandoned).
- Fetches go through the existing `ingest.Service.Start` and are recorded
  as fetch runs with trigger `scheduler`.
- The existing one-running-run-per-source rule prevents overlap, also with
  CLI and API fetches.
- A failing source is logged and retried at its next interval; it never
  stops the others.
- On shutdown the scheduler stops first, then in-flight fetches are
  drained.
- `SCHEDULER_ENABLED=false` turns it off.

Deferred scheduler ideas:

- **Backoff for failing sources.** A source that keeps failing is retried
  every interval. It could wait longer as `consecutive_failures` grows.
- **Global concurrency cap.** Due sources start together. Upstream rate
  limiters already pace requests per API; a cap would matter only with
  many sources.
- **Multiple server instances.** Overlap is prevented per source by the
  database, but two servers would both tick. A leader lock (for example a
  PostgreSQL advisory lock) is needed before running more than one.
- **Abandoned-run recovery delay.** A crashed run is recovered once it is
  older than `FETCH_TIMEOUT` plus about two minutes. Recovery runs on
  every tick, so a crash delays that source by at most that long.

## Phase 2.3: AI enrichment foundation (complete)

Structured intelligence per item, stored apart from `items`:

- **Storage.** Migration `00005` adds `item_enrichments`.
  - One row per item (`item_id` is the primary key and a foreign key to
    `items`).
  - Columns: `topics`, `entities` (name and type), `importance` (1 to 5)
    and `category` from a fixed list.
  - Provenance: provider, model, prompt version and the item's content hash.
- **Code.** `internal/enrich` holds:
  - a minimal `Provider` interface (prompt in, text out)
  - a versioned prompt that treats item text as data
  - strict output parsing and validation against domain bounds
  - a small service
- **Failures.** Malformed or out-of-bounds output is reported and nothing
  is stored. Items are only read, never modified.
- **Idempotent.** An item is selected only if it has no enrichment from
  its current content, model and prompt version. Re-runs do nothing; edited
  items and model changes are picked up.
- **Trigger.** Explicit and bounded: `synergy enrich --fake --limit N`
  (at most 100). Nothing runs automatically.

**Next step: a real provider.** The project had no LLM provider, so only
the interface and a deterministic, offline `FakeProvider` exist. Fake
results are labelled `provider=fake`. A real model's results replace them,
because a different model makes them stale.

Adding the real provider means:

- one `enrich.Provider` implementation (for example Anthropic's Messages
  API)
- an API key from the environment, never logged
- a model name in config
- wiring it into `synergy enrich` in place of the `--fake` requirement

Deferred:

- **Automatic enrichment** of new items (after fetches or on a schedule).
- **Retries or backoff** for provider errors. A failed item is simply
  selected again by the next run.
- **Exposing enrichments** in the API and web app (topics, importance,
  category filters).
- **Batching or concurrency** for large backfills. Runs are sequential and
  capped at 100 items.

## Phase 2.4: item summarization (complete)

Short LLM-written summaries per item, stored apart from `items`:

- **Storage.** Migration `00006` adds `item_summaries`: one row per item
  (`item_id` is the primary key and a foreign key to `items`).
  - The summary is one paragraph of 40 to 600 characters. The database
    rejects newlines and out-of-range lengths.
  - Provenance: provider, model, prompt version and the item's content
    hash.
- **The first real provider.** `enrich.AnthropicProvider` calls Claude
  through the official Go SDK (`anthropic-sdk-go`), implementing the
  Phase 2.3 `Provider` interface.
  - Credentials: `ANTHROPIC_API_KEY` from the environment, never logged.
  - Model: `ANTHROPIC_MODEL`, default `claude-opus-5-5`.
  - Each request runs at low effort with server-side refusal fallbacks
    (`fallbacks: "default"`).
  - A refusal, a reply cut off at the token limit, or an empty reply is an
    error, never a summary.
- **Prompt.** Versioned (`SummaryPromptVersion`). It wraps the item in
  `<item>` tags, says everything inside is untrusted data and must not be
  followed, and asks for plain prose.
- **Validation.** Output is checked before storing: length, a single
  paragraph, and no Markdown, JSON or code fences.
- **Idempotent.** An item is selected only if it has no summary from its
  current content, model and prompt version. Changed items become eligible
  again. A failed summary leaves the item and any existing summary
  untouched.
- **Trigger.** `synergy summarize [--limit N] [--fake]`, default 3 and
  maximum 20 items per run. It uses Claude when `ANTHROPIC_API_KEY` is set;
  `--fake` uses the offline provider. Nothing runs automatically.
- **Tests.** They never call the real API: the provider is tested against
  a local HTTP server, and everything else uses the fake provider.

Deferred:

- **`synergy enrich` with Claude.** The provider exists now, but `enrich`
  still requires `--fake`. Wiring it in is a small follow-up.
- **Automatic summarization** of new items (after fetches or on a
  schedule), and backfills beyond 20 items per run.
- **Showing summaries** in the API and the web app.
- **Retries.** The SDK retries transient errors (twice by default). There
  is no further retry or backoff; a failed item is selected again by the
  next run.

## Phase 2.5: item embeddings (complete)

A vector per item, stored apart from `items`, as the basis for story
clustering:

- **Storage.** Migration `00007` adds `item_embeddings`: one row per item
  and model, the vector as `real[]` with CHECK constraints on its length
  and values, plus provider, model and content-hash provenance.
  - pgvector is not available in the local PostgreSQL 17 install, so
    vectors are plain arrays and cosine similarity
    (`domain.CosineSimilarity`) is computed in Go.
- **Code.** `internal/embed` holds a minimal `Provider` interface, the
  input text (title and description), and a small service.
- **Providers.**
  - `VoyageProvider` calls the Voyage AI embeddings API (Anthropic has no
    embeddings API). Credentials: `VOYAGE_API_KEY`, never logged. Model:
    `VOYAGE_MODEL`, default `voyage-3.5`, at 1024 dimensions. It is tested
    against a local HTTP server only; no live call has been made, because
    no key is configured.
  - `FakeProvider` is deterministic and offline, for tests and for trying
    the pipeline. Its vectors only reflect shared words.
- **Validation.** Wrong length, non-finite values and zero vectors are
  rejected before storing, and again by the database.
- **Idempotent.** An item is selected only if it has no embedding for the
  model from its current content. Changed items are re-embedded. A failed
  call leaves the item and any existing embedding untouched.
- **Trigger.** `synergy embed [--limit N] [--fake]`, default 10 and
  maximum 50 items per run. Nothing runs automatically.

Deferred:

- **pgvector and an index**, once the item count makes in-Go similarity
  too slow, or for semantic search.
- **Batching.** Voyage accepts many inputs per request; one item per call
  is simpler and sufficient for runs of at most 50.
- **Automatic embedding** of new items, and backfills beyond 50 per run.

## Phase 2.6: story clustering (complete)

Groups of items about the same underlying story, built from the Phase 2.5
embeddings and stored apart from `items`:

- **Storage.** Migration `00008` adds `stories` (id, embedding model, seed
  item) and `story_items` (item, story, model, similarity to the seed).
  - An item belongs to at most one story per model (primary key `item_id,
    model`). A composite foreign key makes a membership's model match its
    story's model. Similarity is checked to lie in [-1, 1].
- **Algorithm.** Single-pass "leader" clustering in `internal/cluster`:
  - Items are taken oldest first. Each is compared, by cosine similarity,
    with the seed (first item) of each of the model's 1,000 newest
    stories.
  - It joins the most similar story if the similarity is at least the
    threshold (ties go to the newest story); otherwise it starts a new
    story with itself as seed.
  - Stories never merge or split, and memberships are never revised.
- **Threshold.** Default 0.80, adjustable per run with `--threshold`. It is
  an initial guess, not a tuned value. The only real-data evidence so far
  is three unrelated items embedded with `voyage-4-lite` during
  verification, whose pairwise similarities were 0.39 to 0.49. How high
  same-story pairs score has not been measured yet.
- **Safety.**
  - Only items with a current embedding for the model (same content hash
    and dimensions) are selected. Missing or stale embeddings are waited
    for, never invented.
  - Models and dimensions are never mixed: stories are per model, and
    incompatible vectors are rejected or skipped.
  - A failure affects one item only. Items are never modified.
- **Idempotent.** Clustered items are not selected again, so repeated runs
  add nothing.
- **Trigger.** `synergy cluster [--limit N] [--threshold T] [--fake]`,
  default 20 and maximum 200 items per run. It uses the `VOYAGE_MODEL`
  embeddings, or the fake provider's with `--fake`. It makes no API calls.
  Nothing runs automatically.

Limitations, deliberately accepted for now:

- **Order dependence.** The result depends on which item came first. The
  seed is the only representative, so a story whose later reports drift
  from the first one may split.
- **No re-clustering.** An item whose content changes after clustering
  keeps its story. Changing the threshold affects only items clustered
  afterwards. Resetting means deleting a model's stories.
- **Bounded comparison.** Items are compared in Go with up to 1,000 story
  seeds. That is fine at the current scale; pgvector or a time window
  would be needed for much more.
- **Not shown anywhere yet.** Stories are not exposed in the API or the
  web app.

## Phase 2.7 and later: intelligence (future, not started)

This section records intended direction only. Nothing here is implemented,
and the order and scope will be decided when each step is planned and
approved.

Guiding principle: AI-derived and per-user data live in **new tables** keyed
by item or user. The Phase 1 tables (`sources`, `items`, `fetch_runs`) and
the ingestion pipeline stay as they are, and enrichment runs downstream of
ingestion.

### Likely first steps

1. **Semantic search** over the Phase 2.5 embeddings.
2. **Ranking.**
   - A transparent, explainable score that combines source signal (points,
     stars, cross-source sightings), recency and topic relevance.
   - Exposed as an alternative feed order next to the chronological one.

### Later

- **Users and authentication.** Needed before Synergy is exposed beyond
  localhost. This also brings per-user preferences, saved items and
  feedback (likes, hides) in their own tables.
- **Personalization.** Rank with the user's interests and feedback.
- **Source discovery.** Suggest new sources and new source types (blogs,
  RSS, newsletters, release feeds) through the existing adapter contract.
- **Operations.**
  - Metrics (fetch durations, failures, items per source).
  - Retention policies for fetch runs.
  - A deployable configuration (TLS, a non-loopback bind behind auth).

### Known follow-ups carried over from Phase 1

These are small and non-blocking. They are listed so they are not forgotten.

- A GitHub fetch facing a dead network spends its retries on every query
  (about a minute without a token) before failing. It is bounded by
  `FETCH_TIMEOUT`, but it could stop after the first transport failure, as
  it already does for rate limits.
- A Hacker News fetch is marked failed if any single item request fails,
  even though the other items are stored. A failure threshold may suit a
  scheduler better.
- Fetch-run error texts can include PostgreSQL messages when storing fails.
  That is fine locally; it should be revisited when the API is exposed to
  other users.
