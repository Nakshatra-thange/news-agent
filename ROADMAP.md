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

## Phase 2: intelligence (future, not started)

This section records intended direction only. Nothing here is implemented,
and the order and scope will be decided when Phase 2 is planned.

Guiding principle: AI-derived and per-user data live in **new tables** keyed
by item or user. The Phase 1 tables (`sources`, `items`, `fetch_runs`) and
the ingestion pipeline stay as they are, and enrichment runs downstream of
ingestion.

### Likely first steps

1. **Scheduler.**
   - Fetch active sources automatically, respecting `min_fetch_interval`,
     priority and per-upstream budgets.
   - Back off sources whose health is `failing`.
   - Reuse `ingest.Service.Start` with `trigger=scheduler`; the schema
     already allows it.
2. **Enrichment pipeline.**
   - An asynchronous job queue (PostgreSQL-backed, for example with
     `FOR UPDATE SKIP LOCKED`) that processes newly inserted items.
   - Enrichments are versioned per model and prompt, so they can be
     recomputed.
3. **Summaries.**
   - Short LLM-generated summaries stored in an `item_summaries` table.
   - Cost-bounded: only for items that pass cheap filters.
4. **Embeddings and semantic search.**
   - Store item embeddings (for example with pgvector) to enable semantic
     search.
   - Use them for story-level deduplication: the same news at different
     URLs.
5. **Clustering.** Group items about the same development across sources,
   building on the exact-URL `duplicate_of` links Phase 1 already keeps.
6. **Ranking.**
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
- **Frontend** over the existing feed API.
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
