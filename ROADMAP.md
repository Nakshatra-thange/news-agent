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

## Phase 2.3 and later: intelligence (future, not started)

This section records intended direction only. Nothing here is implemented,
and the order and scope will be decided when each step is planned and
approved.

Guiding principle: AI-derived and per-user data live in **new tables** keyed
by item or user. The Phase 1 tables (`sources`, `items`, `fetch_runs`) and
the ingestion pipeline stay as they are, and enrichment runs downstream of
ingestion.

### Likely first steps

1. **Enrichment pipeline.**
   - An asynchronous job queue (PostgreSQL-backed, for example with
     `FOR UPDATE SKIP LOCKED`) that processes newly inserted items.
   - Enrichments are versioned per model and prompt, so they can be
     recomputed.
2. **Summaries.**
   - Short LLM-generated summaries stored in an `item_summaries` table.
   - Cost-bounded: only for items that pass cheap filters.
3. **Embeddings and semantic search.**
   - Store item embeddings (for example with pgvector) to enable semantic
     search.
   - Use them for story-level deduplication: the same news at different
     URLs.
4. **Clustering.** Group items about the same development across sources,
   building on the exact-URL `duplicate_of` links Phase 1 already keeps.
5. **Ranking.**
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
