# Synergy architecture (Phase 1)

This document describes how the Phase 1 backend is built and why. README.md
covers setup and usage. AGENTS.md has the rules for changing the code.

## Overview

Synergy is one Go binary, `bin/synergy`, backed by one PostgreSQL database.

```
            upstream APIs                         clients (curl, future frontend)
   Hacker News   arXiv   GitHub                               |
        |          |        |                                 v
        v          v        v                       +-------------------+
   +-----------------------------+                  |  internal/api     |
   | internal/sources/<type>     |  candidates      |  routes, JSON,    |
   | adapters (HTTP + parsing)   |-------+          |  errors, request  |
   +--------------^--------------+       |          |  IDs, access log  |
                  | httpx: rate limit,   v          +----+---------+----+
                  | retry, redaction  +-------------------+  |         |
                  +-------------------| internal/ingest   |<-+ fetch   | feed, sources
                                      | normalize, canon, |            |
   synergy fetch (CLI) -------------->| dedup, run        |            |
                                      | bookkeeping       |            |
                                      +---------+---------+            |
                                                v                      v
                                      +-----------------------------------+
                                      | internal/store (pgx, goose)       |
                                      | the only package that speaks SQL  |
                                      +-----------------+-----------------+
                                                        v
                                                  PostgreSQL 17
```

### Subcommands (`cmd/synergy`)

| Command | What it does |
|---|---|
| `serve` | HTTP API. Starts even if PostgreSQL is down; `/health` stays up and `/health/db` reports the problem. |
| `migrate up\|status\|version\|down --yes` | Applies the migrations embedded in the binary, under a PostgreSQL advisory lock. |
| `seed` | Registers each source type's default sources. Idempotent; never overwrites. |
| `fetch <slug>... \| --all [--force]` | Runs ingestion synchronously and prints one summary line per source. |
| `version` | Prints the build version. |

`cmd/synergy` is the only place that knows the concrete source types: it
lists their specs in `sourceTypes()` and their adapters in `adapters()`.

## Packages

| Package | Responsibility | Depends on |
|---|---|---|
| `config` | Reads environment variables, applies defaults, reports every invalid value at once. Secret values are redacted from `slog`. | stdlib |
| `domain` | Core types (`Source`, `Item`, `FetchRun`, `Candidate`, filters), their validation, and sentinel errors (`ErrNotFound`, `ErrConflict`, `ErrInvalid`, `ErrTooSoon`, `ErrUnsupported`, `ErrFetchInProgress`, `ErrUnavailable`). No I/O. | stdlib, uuid |
| `canon` | URL canonicalization (identity key for dedup) and text cleaning. Pure functions. | stdlib |
| `sources` | Source registry and its rules: types, per-type config validation, fetch policy, status lifecycle, seeding. Also the `Adapter` contract and shared client options. | domain, httpx |
| `sources/<type>` | One package per type: `Config`/`Spec` (defaults, strict parsing, politeness floor, seeds) and `Adapter` (HTTP + parsing, no database). | sources, httpx, domain |
| `httpx` | Outbound HTTP for adapters: per-upstream token buckets, bounded retries with jittered backoff and `Retry-After`, per-attempt timeout, 10 MiB response cap, errors without headers or query strings. | stdlib, x/time/rate |
| `ingest` | The source-agnostic fetch pipeline and fetch-run lifecycle (sync `Run`, async `Start`, `Shutdown`, abandoned-run recovery). | domain, canon, sources |
| `store` | PostgreSQL access through a pgx pool. Maps database errors to domain errors and runs the embedded goose migrations. | domain, pgx, goose |
| `api` | `net/http` handlers and middleware: request IDs, access log, panic recovery, strict JSON and query parsing, consistent error bodies. No business logic. | domain, ingest (types), sources (types) |

Interfaces are declared where they are used. For example, `ingest.Store`,
`sources.Store` and `api.ItemReader` each list only the methods their
package needs, and `*store.Store` satisfies them all.

## Data model

Three tables. Migrations live in `migrations/`; `00001_core_schema.sql` is the
authoritative definition.

- **`sources`**: a configured instance of a source type. There can be several
  per type.
  - Identity and settings: `slug` (unique, immutable), `type` (immutable),
    `status` (`active`/`paused`/`retired`), `priority`, `config` (adapter
    settings, validated in Go), `min_fetch_interval_seconds`.
  - Fetch progress: `state`, the adapter's cursor, written only after
    successful fetches.
  - Health: `last_fetch_at`, `last_success_at`, `last_failure_at`,
    `last_error`, `consecutive_failures`.
  - Sources are retired, never deleted, so item provenance survives.
- **`items`**: one normalized piece of content.
  - Identity: `(source_id, external_id)` is unique, the same-source dedup key.
  - Cross-source dedup: `url_hash` = sha256 of the canonical URL. A new item
    whose `url_hash` matches a primary item from another source is stored
    with `duplicate_of` pointing at it.
  - `content_hash` detects edits.
  - `feed_at` is a generated column, `COALESCE(published_at, discovered_at)`,
    so the feed order key is never NULL.
  - Source-specific numbers (stars, points, PDF link, ...) go in `metadata`
    (jsonb).
- **`fetch_runs`**: one attempt to fetch one source.
  - Records `trigger` (`api`/`cli`/`scheduler`), `status`
    (`running`/`succeeded`/`failed`), per-outcome counts and `error`.
  - A partial unique index allows at most one `running` run per source, so
    the database itself prevents concurrent fetches of the same source.

Open-ended vocabularies (`type`, `kind`) are format-checked in SQL and
validated in Go, so adding a source type needs no migration. Closed
lifecycle vocabularies (statuses, triggers) are enforced by CHECK
constraints.

Indexes and the queries they serve:

| Index | Query |
|---|---|
| `items_feed_idx (feed_at DESC, id DESC) WHERE duplicate_of IS NULL` | default feed and keyset pagination |
| `items_source_feed_idx (source_id, feed_at DESC, id DESC)` | feed filtered by source; FK |
| `items_url_hash_idx (url_hash)` | cross-source dedup lookup at insert |
| `items_duplicate_of_idx WHERE duplicate_of IS NOT NULL` | `also_seen_on` sightings; FK |
| `items_tags_idx` GIN | `tag=` filter (`tags @> ...`) |
| `items_search_idx` GIN on `to_tsvector('english', title \|\| ' ' \|\| description)` | `q=` full-text search |
| `fetch_runs_one_running_per_source` (partial unique) | one in-flight fetch per source |
| `fetch_runs_source_started_idx` | run history per source; FK |

## Fetch flow

`ingest.Service.Run` (CLI, synchronous) and `Start` (API, background) share
one pipeline:

1. **Pre-flight.**
   - The source must exist and be `active`, and an adapter must be
     registered for its type.
   - Unless forced, the cooldown must have passed.
   - `StartFetchRun` inserts the `running` run and stamps `last_fetch_at` in
     one transaction. The partial unique index turns a concurrent second
     fetch into `ErrFetchInProgress` (409).
2. **Fetch.**
   - `adapter.Fetch` runs under `FETCH_TIMEOUT`, cancelled by the caller or
     by `Shutdown`.
   - Adapter panics are recovered into a failed run.
   - Adapters return partial results together with an error.
3. **Normalize.** Clean the text, validate, canonicalize the URL, hash,
   reject unrepairable candidates and fold repeats.
4. **Store.**
   - `UpsertItems` writes the whole batch in one transaction, on a context
     detached from fetch cancellation so already-fetched items are not lost.
   - One SQL statement per item does the insert-or-update, the
     unchanged-skip and the cross-source dedup link.
5. **Finish.**
   - `FinishFetchRun` records counts and outcome, and updates source health in
     the same transaction.
   - Success resets `consecutive_failures` and saves the adapter `state`.
     Failure increments the streak and keeps the old `state`, so the cursor
     never advances past data that was not fully fetched.

Recovery: at `serve` and `fetch` startup, `RecoverAbandoned` fails `running`
runs older than `FETCH_TIMEOUT` plus the store and finish bounds plus a
minute. It also records the failure in each affected source's health, in the
same statement. Runs from a live process are never that old.

Shutdown (`serve`):

1. The HTTP server drains within `SHUTDOWN_TIMEOUT`.
2. The ingest service stops accepting fetches and waits, again within
   `SHUTDOWN_TIMEOUT`, then cancels what is still running. Cancelled runs
   are still recorded as failed (`fetch aborted: service shutting down`).
3. Only then is the database pool closed.

## Feed flow

`GET /api/v1/items`:

1. The query string is parsed strictly. Unknown or repeated parameters and
   malformed values return 400 with every problem listed.
2. Source slugs are resolved to IDs through the registry.
3. `store.ListItems` builds one parameterized statement. A CTE selects the
   page (filters, `ORDER BY feed_at DESC, id DESC`, `LIMIT n+1`) joined with
   its source. A correlated subquery then aggregates each returned item's
   cross-source sightings as JSON. The result is one round trip with no N+1.
4. The cursor is base64url JSON holding a version, the last row's
   `(feed_at, id)` and a fingerprint of the filters. A cursor reused with
   other filters, tampered with or from another version returns 400. The
   next page is `(feed_at, id) < (cursor)`, which is stable while new items
   arrive.

Defaults: only `active` sources, cross-source duplicates hidden, 50 items
(maximum 200).

## Errors and logging

- Store methods return domain sentinels. The API maps them in one place,
  `writeServiceError`:
  - 404 for `ErrNotFound`
  - 409 for `ErrConflict` and `ErrFetchInProgress`
  - 422 for validation errors
  - 429 for `ErrTooSoon`, with `Retry-After`
  - 501 for `ErrUnsupported`
  - 503 for `ErrUnavailable` (database unreachable) and during shutdown
  - 500 for anything else, with a generic message; details are logged under
    the request ID
- Logs are structured `slog` (JSON by default). Credentials are never
  logged:
  - Config types implement `LogValue` with redaction.
  - The database is identified only by host, port, database and user.
  - httpx errors omit headers and query strings.
  - The GitHub token travels only in the `Authorization` header.
- Health probes log at debug level so monitors don't flood the log.

## Security posture (Phase 1)

- No authentication. The server binds to `127.0.0.1` by default and warns at
  startup when it listens anywhere else.
- Request bodies are capped at 1 MiB, JSON is decoded strictly, query strings
  are parsed strictly, and HTTP server timeouts are set.
- All SQL is parameterized. The only `fmt.Sprintf` into SQL inserts
  fixed, code-defined fragments (WHERE clauses built from constants and
  `$n` placeholders).
- The `synergy` PostgreSQL role is not a superuser and cannot create
  databases or roles. It owns only its two databases.
- `.env` is git-ignored and written with mode 600 by `make db-setup`.

## Design decisions

- **Keyset pagination, not OFFSET.** Pages stay stable while items arrive,
  and the cost is independent of depth.
- **Duplicates are stored, not dropped.** Each one is linked with
  `duplicate_of`. The feed hides them but exposes them as `also_seen_on`, so
  cross-source signal is kept for later ranking.
- **No auto-migrate on `serve`.** Schema changes are an explicit step.
  `/health/db` reports `migrations_pending`.
- **Partial-failure semantics.** Store what was fetched, mark the run
  failed, and don't advance the cursor. No data is lost, and nothing is
  silently skipped.
- **Shared per-upstream rate limiters.** Upstreams limit per client, not per
  Synergy source.
- **Synchronous CLI and asynchronous API fetches over one service.** A
  scheduler (a later phase) can call `Start` with `trigger=scheduler` without
  pipeline changes.
- **Dependencies are minimal:**
  - `pgx` for PostgreSQL
  - `goose` for migrations
  - `google/uuid` for UUIDv7 keys
  - `x/time/rate` for token buckets
