# Synergy

Synergy is a personalized AI-development intelligence platform. It continuously
discovers important developments in AI from many sources, filters and ranks
them according to your interests, and presents a concise feed that links back
to the original sources.

## Status

**Phase 1, Stage 3 (source registry) complete.** Synergy persists its core
data in PostgreSQL and manages its information sources through a registry
with a REST API: register, configure, prioritize, pause, retire and restore
sources, with per-type configuration validation and health reporting.
Fetching, the ingestion pipeline and the feed API arrive in later stages.

Phase 1 scope: a Go backend that registers sources (GitHub, Hacker News,
arXiv), fetches and normalizes their content, deduplicates it, stores it in
PostgreSQL and exposes it over a REST API. No AI, users, auth, frontend or
scheduler yet.

## Requirements

- Go 1.26+
- PostgreSQL 17, running locally (no Docker needed)

## Quick start

```sh
make db-setup            # one-time: creates role + databases, writes .env
make migrate             # applies migrations to the synergy database
make seed                # registers the default Hacker News, arXiv and GitHub sources
make run                 # builds bin/synergy and starts the server
curl localhost:8080/health/db
# {"status":"ok","schema_version":1,"latest_version":1,"latency_ms":1}
curl localhost:8080/api/v1/sources
```

## Database setup

`make db-setup` runs `scripts/db-setup.sh`, which is idempotent:

- creates a `synergy` login role (no superuser, createdb or createrole rights)
- creates the `synergy` and `synergy_test` databases owned by that role
- writes `DATABASE_URL` and `TEST_DATABASE_URL` with a generated password into
  `.env` (git-ignored, mode 600). Re-runs reuse the existing password.

It needs PostgreSQL superuser access once: `psql` prompts for the password on
your terminal, or uses `PGPASSWORD` / `~/.pgpass`. Override the defaults with
`PGHOST`, `PGPORT` or `PG_SUPERUSER` (default `postgres`).

## Migrations

Migrations live in `migrations/` as numbered SQL files and are embedded in the
binary, so a deployed binary always carries its own schema.

```sh
synergy migrate            # same as `migrate up`: apply pending migrations
synergy migrate status     # list migrations and whether each is applied
synergy migrate version    # current vs latest schema version
synergy migrate down --yes # roll back the latest migration (destroys data)
```

A PostgreSQL advisory lock serializes concurrent migration runs. The server
does not migrate automatically: if the schema is behind, it logs a warning and
`/health/db` reports `migrations_pending`.

## Sources

A **source** is a configured instance of a **source type**. There can be
several sources of one type (say, two arXiv sources with different
categories). `synergy seed` registers one recommended source per type; it is
idempotent and never overwrites a source you have edited.

| Seed slug | Type | Priority | Fetch interval | What it covers |
|---|---|---|---|---|
| `hn-ai` | `hackernews` | 50 | 30m | HN stories matching AI keywords, at least 30 points, last 2 days |
| `arxiv-ai` | `arxiv` | 40 | 3h | New papers in cs.AI, cs.LG, cs.CL, last 3 days |
| `github-ai-repos` | `github` | 30 | 1h | Repos created in the last 7 days with AI topics and at least 50 stars |

**Priority** (-1000 to 1000) orders listings, and later the fetch order.
Higher comes first.

**Status** follows a small lifecycle:

```
active <-> paused        (pause / resume)
active|paused -> retired (retire: the source is frozen, its items are kept)
retired -> active        (restore; the only change a retired source accepts)
```

Sources are never deleted, so the provenance of their items is preserved.

**Health** is derived from fetch history: `unknown` (never fetched),
`healthy`, `degraded` (1-2 consecutive failures) or `failing` (3 or more).

**Configuration** is validated per type, and unknown fields are rejected so
typos surface. Omitted fields take the type's defaults, and the stored config
always shows the effective values. Durations accept `"90m"`, `"48h"` or `"7d"`.

| Type | Fields (defaults) | Min interval |
|---|---|---|
| `hackernews` | `queries` (8 AI keywords, 1-10), `min_points` (30), `lookback` ("2d", 1h-30d), `max_results_per_query` (50, max 200) | 5m |
| `arxiv` | `categories` (["cs.AI","cs.LG","cs.CL"], 1-20), `max_results` (100, max 500), `lookback` ("3d", 1h-30d) | 30m |
| `github` | `queries` (AI topics, 1-10; no `created:`/`stars:`/`sort:` qualifiers), `created_within` ("7d", 1d-365d), `min_stars` (50), `sort` ("stars" or "updated"), `max_results_per_query` (30, max 100) | 10m |

The minimum interval is a politeness floor: no source can be configured to
fetch its upstream API more often than that.

## Configuration

All configuration is via environment variables (see `.env.example`).

| Variable | Default | Description |
|---|---|---|
| `SERVER_HOST` | `127.0.0.1` | Bind address. Loopback by default: Phase 1 has no auth. |
| `SERVER_PORT` | `8080` | Listen port (`0` picks a free port). |
| `SHUTDOWN_TIMEOUT` | `15s` | Grace period for in-flight requests on shutdown. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. |
| `DATABASE_URL` | (required) | `postgres://` URL for the application database. Never logged. |
| `TEST_DATABASE_URL` | (unset) | Integration-test database; its name must end in `_test`. |
| `DB_MAX_CONNS` | `10` | Maximum pool connections. |
| `DB_MIN_CONNS` | `0` | Connections kept open when idle. |
| `DB_MAX_CONN_LIFETIME` | `1h` | Connections are recycled after this age. |
| `DB_MAX_CONN_IDLE_TIME` | `30m` | Idle connections are closed after this. |
| `DB_CONNECT_TIMEOUT` | `5s` | Timeout for establishing a connection. |

Invalid values stop startup with a message listing every problem.

## Development

```sh
make help              # list targets
make test              # all tests
make test-integration  # PostgreSQL integration tests only (verbose)
make check             # gofmt, go vet, staticcheck, race-enabled tests
```

Integration tests in `internal/store` run against `TEST_DATABASE_URL` (the
Makefile loads it from `.env`) and are skipped when it is unset. Each test
creates its own schema, migrates it, and drops it afterwards, so tests are
isolated and leave nothing behind. They refuse to run against a database
whose name does not end in `_test`.

## API

The server binds to `127.0.0.1` by default. Phase 1 has no authentication.

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness probe. Does not touch dependencies. |
| GET | `/health/db` | Pings PostgreSQL and checks the schema version. `200` with `status: ok`; `503` with `status: unavailable` or `migrations_pending`. |
| GET | `/api/v1/source-types` | Supported types with descriptions, default config and fetch policy. |
| GET | `/api/v1/sources` | List sources, highest priority first. `?status=` (repeatable or comma-separated: `active`, `paused`, `retired`, `all`; default active and paused). `?type=`. |
| POST | `/api/v1/sources` | Register a source. Returns `201` with a `Location` header. |
| GET | `/api/v1/sources/{ref}` | Get one source; `{ref}` is its UUID or slug. |
| PATCH | `/api/v1/sources/{ref}` | Partial update of `name`, `url`, `status`, `priority`, `config`, `min_fetch_interval_seconds`. Absent or `null` fields are unchanged. A new `config` replaces the old one. `slug` and `type` are immutable. |

### Examples

```sh
# Register a second arXiv source (omitted config fields get defaults)
curl -X POST localhost:8080/api/v1/sources -H 'Content-Type: application/json' -d '{
  "slug": "arxiv-robotics", "name": "arXiv: Robotics", "type": "arxiv",
  "priority": 20, "config": {"categories": ["cs.RO"]}
}'

# Pause, resume, retire, restore
curl -X PATCH localhost:8080/api/v1/sources/arxiv-robotics -H 'Content-Type: application/json' -d '{"status":"paused"}'
curl -X PATCH localhost:8080/api/v1/sources/arxiv-robotics -H 'Content-Type: application/json' -d '{"status":"active"}'
curl -X PATCH localhost:8080/api/v1/sources/arxiv-robotics -H 'Content-Type: application/json' -d '{"status":"retired"}'

# Re-prioritize and reconfigure
curl -X PATCH localhost:8080/api/v1/sources/hn-ai -H 'Content-Type: application/json' \
  -d '{"priority": 80, "config": {"queries": ["LLM", "Anthropic"], "min_points": 100}}'
```

A source looks like this (abridged):

```json
{
  "id": "01a11070-adc4-713c-b6b4-58be49c78734",
  "slug": "arxiv-ai", "name": "arXiv: AI, ML & NLP", "type": "arxiv",
  "url": "https://arxiv.org", "status": "active", "priority": 40,
  "config": {"categories": ["cs.AI", "cs.LG", "cs.CL"], "max_results": 100, "lookback": "3d"},
  "state": {},
  "min_fetch_interval_seconds": 10800,
  "health": {"status": "unknown", "consecutive_failures": 0, "last_fetch_at": null,
             "last_success_at": null, "last_failure_at": null, "last_error": ""},
  "created_at": "2026-10-06T08:59:42.404155Z", "updated_at": "...", "retired_at": null
}
```

### Errors

Every error has the same shape. Clients should branch on `code`:

```json
{"error": {"code": "validation_failed", "message": "request validation failed",
           "details": [{"field": "config.categories", "message": "\"CS AI\" is not an arXiv category such as cs.LG"}]}}
```

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_json` | Malformed body, unknown or immutable field, wrong JSON type |
| 400 | `invalid_query` | Bad query parameter (with `details`) |
| 404 | `not_found` | Unknown route or resource |
| 405 | `method_not_allowed` | Wrong method (with an `Allow` header) |
| 409 | `conflict` | Duplicate slug, disallowed status transition, editing a retired source |
| 413 | `payload_too_large` | Body over 1 MiB |
| 415 | `unsupported_media_type` | Content-Type is not `application/json` |
| 422 | `validation_failed` | Field values invalid (all problems listed in `details`) |
| 500 | `internal` | Unexpected error; details are logged under the request ID |

Every response carries an `X-Request-ID` header (an incoming one is reused if
well-formed).

## Documentation

- `AGENTS.md`: rules for contributors and coding agents
- `ARCHITECTURE.md`, `ROADMAP.md`: added in Stage 7
