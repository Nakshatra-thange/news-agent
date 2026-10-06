# Synergy

Synergy is a personalized AI-development intelligence platform. It continuously
discovers important developments in AI from many sources, filters and ranks
them according to your interests, and presents a concise feed that links back
to the original sources.

## Status

**Phase 1, Stage 2 (database layer) complete.** The service persists its core
data (sources, items, fetch runs) in PostgreSQL via embedded, versioned
migrations, and reports database health. Source fetching, the ingestion
pipeline and the feed API arrive in later stages.

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
make run                 # builds bin/synergy and starts the server
curl localhost:8080/health/db
# {"status":"ok","schema_version":1,"latest_version":1,"latency_ms":1}
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

## API (so far)

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness probe. Does not touch dependencies. |
| GET | `/health/db` | Pings PostgreSQL and checks the schema version. `200` with `status: ok`; `503` with `status: unavailable` or `migrations_pending`. |

Every response carries an `X-Request-ID` header (an incoming one is reused if
well-formed). Errors are JSON: `{"error":{"code":"not_found","message":"..."}}`.

## Documentation

- `AGENTS.md`: rules for contributors and coding agents
- `ARCHITECTURE.md`, `ROADMAP.md`: added in Stage 7
