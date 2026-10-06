# Synergy

Synergy is a personalized AI-development intelligence platform. It continuously
discovers important developments in AI from many sources, filters and ranks
them according to your interests, and presents a concise feed that links back
to the original sources.

## Status

**Phase 1, Stage 1 (skeleton) complete.** The service starts, serves `GET
/health`, logs structurally and shuts down gracefully. Database, source
ingestion and the feed API arrive in later stages.

Phase 1 scope: a Go backend that registers sources (GitHub, Hacker News,
arXiv), fetches and normalizes their content, deduplicates it, stores it in
PostgreSQL and exposes it over a REST API. No AI, users, auth, frontend or
scheduler yet.

## Requirements

- Go 1.26+
- PostgreSQL 17 (from Stage 2)

## Quick start

```sh
cp .env.example .env     # adjust if needed
make run                 # builds bin/synergy and starts the server
curl localhost:8080/health
# {"status":"ok","version":"dev"}
```

## Configuration

All configuration is via environment variables (see `.env.example`).

| Variable | Default | Description |
|---|---|---|
| `SERVER_HOST` | `127.0.0.1` | Bind address. Loopback by default: Phase 1 has no auth. |
| `SERVER_PORT` | `8080` | Listen port (`0` picks a free port). |
| `SHUTDOWN_TIMEOUT` | `15s` | Grace period for in-flight requests on shutdown. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `json` | `json` or `text`. |

Invalid values stop startup with a message listing every problem.

## Development

```sh
make help        # list targets
make test        # unit tests
make check       # gofmt, go vet, staticcheck, race-enabled tests
```

## API (so far)

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness probe. Does not touch dependencies. |

Every response carries an `X-Request-ID` header (an incoming one is reused if
well-formed). Errors are JSON: `{"error":{"code":"not_found","message":"..."}}`.

## Documentation

- `AGENTS.md`: rules for contributors and coding agents
- `ARCHITECTURE.md`, `ROADMAP.md`: added in Stage 7
