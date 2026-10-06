# AGENTS.md

Guidance for AI coding agents (and humans) working on Synergy.

## What this is

Synergy is a personalized AI-development intelligence platform. **Phase 1** is a
Go backend that registers sources (GitHub, Hacker News, arXiv), fetches and
normalizes their content into a common Item model, deduplicates it, stores it in
PostgreSQL, and serves it over a REST API. See README.md and ARCHITECTURE.md.

## Workflow rules

- Work is delivered in **stages**. Finish one stage, then run `make check`,
  verify the running application, summarize, and **stop for approval** before
  starting the next stage.
- Do not add out-of-scope features: no AI/LLM calls, users, auth, frontend,
  scheduler or personalization in Phase 1. Note ideas in ROADMAP.md instead.
- Do not add sources beyond GitHub, Hacker News and arXiv in Phase 1.
- No Docker dependency. Development uses a local PostgreSQL 17 install.
- Never commit secrets. Configuration comes from environment variables;
  document every new variable in `.env.example` and README.md.

## Commands

| Task | Command |
|---|---|
| Build | `make build` |
| Run server | `make run` (reads `.env` if present) |
| One-time DB setup | `make db-setup` (needs PostgreSQL superuser password once) |
| Apply migrations | `make migrate` |
| Register default sources | `make seed` |
| All tests | `make test` (integration tests run when `TEST_DATABASE_URL` is set) |
| Integration tests | `make test-integration` |
| Everything (gofmt, vet, staticcheck, race tests) | `make check` |

## Architecture rules

Layout: `cmd/synergy` (wiring and subcommands) and `internal/{config, domain,
canon, ingest, sources, httpx, store, api}`.

- Business logic lives in `domain`, `canon` and `ingest`. It must not import
  `net/http` handlers, `store`, or concrete source packages.
- `api` handles HTTP only: parse, validate, call a service, write JSON.
- `store` is the only package that speaks SQL.
- Source adapters (`sources/<name>`) do HTTP + parsing only; no database access
  and no dedup logic. Keep parsing in pure functions tested with fixtures.
- Define interfaces where they are consumed, not where they are implemented.
- Prefer the standard library. Adding a dependency needs a clear reason.
  Current dependencies: pgx/v5 (PostgreSQL), goose/v3 (migrations),
  google/uuid (UUIDv7 IDs).

## Adding a source type

1. Add the type constant to `domain.SourceTypes` (`internal/domain/source.go`).
2. Create `internal/sources/<name>/config.go` with a `Config` struct,
   `DefaultConfig()`, a strict `ParseConfig()` (decode on top of the defaults
   with `sources.DecodeConfig`, then range-check with `sources.Checks`) and a
   `Spec` implementing `sources.TypeSpec` (description, fetch policy with a
   politeness floor, seed sources).
3. Call `sourcestest.Conformance(t, Spec{})` from its tests, plus tests for
   its own fields.
4. Register the spec in `sourceTypes()` in `cmd/synergy/seed.go`.
5. Later stages add the fetch adapter in the same package.

No database migration is needed: source type and config are open-ended in
the schema and validated in Go.

## Database rules

- Schema changes go in a new numbered file in `migrations/`
  (`NNNNN_description.sql` with `-- +goose Up` / `-- +goose Down`). Never edit
  a migration that has been applied anywhere.
- Every migration must be reversible and pass `TestMigrateDownIsReversible`.
- Store methods return domain errors (`domain.ErrNotFound`, `ErrConflict`,
  `ErrInvalid`, `ErrFetchInProgress`); callers use `errors.Is`.
- AI-derived and per-user data (summaries, scores, topics, feedback) belong in
  their own future tables, not new columns on `items`.
- Never log `DATABASE_URL` or any credential. Log `store.Target()` instead.

## Conventions

- Logging: `log/slog`, structured key/value attributes, no `fmt.Println`.
  Info for lifecycle and fetch summaries, Debug for chatty detail.
- Errors: wrap with context (`fmt.Errorf("load source %s: %w", id, err)`).
  API errors use `{"error":{"code","message"}}`.
- Every outbound HTTP request takes a `context.Context` and has a timeout.
- Tests: table-driven, standard library only, no network. External API
  responses are recorded in `testdata/` fixtures.
- Code must pass `gofmt -s`, `go vet` and `staticcheck`.
