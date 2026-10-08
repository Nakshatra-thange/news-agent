# AGENTS.md

Guidance for AI coding agents (and humans) working on Synergy.

## What this is

Synergy is a personalized AI-development intelligence platform. **Phase 1** is a
Go backend that registers sources (GitHub, Hacker News, arXiv), fetches and
normalizes their content into a common Item model, deduplicates it, stores it in
PostgreSQL, and serves it over a REST API. Phase 1 is complete. Phase 2.1
added a Next.js web app in `web/`. Each further Phase 2 step needs explicit
approval before any planning or code. See
README.md, ARCHITECTURE.md and ROADMAP.md.

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
| All tests with PostgreSQL integration tests required (race on) | `make test-integration` |
| Live API smoke tests (opt-in, hits real APIs) | `go test -tags live -count=1 ./internal/sources/...` |
| Everything (gofmt, vet, staticcheck, race tests) | `make check` |

## Architecture rules

Layout: `cmd/synergy` (wiring and subcommands) and `internal/{config, domain,
canon, ingest, scheduler, enrich, sources, httpx, store, api}`.

- `enrich` reads items and writes only `item_enrichments` and
  `item_summaries`. Model output is untrusted: parse it strictly and
  `Validate` before storing. Tests use `FakeProvider`, scripted providers
  or a local HTTP server, and never call a real LLM.

- `scheduler` only decides *when* to fetch; it calls `ingest.Service.Start`
  and must never duplicate fetch, dedup or run-bookkeeping logic.

- `ingest` is source-agnostic: it must never import a concrete source
  package or branch on a source type.
- Business logic lives in `domain`, `canon` and `ingest`. It must not import
  `net/http` handlers, `store`, or concrete source packages.
- `api` handles HTTP only: parse, validate, call a service, write JSON.
- `store` is the only package that speaks SQL.
- Source adapters (`sources/<name>`) do HTTP + parsing only; no database access
  and no dedup logic. Keep parsing in pure functions tested with fixtures.
- Define interfaces where they are consumed, not where they are implemented.
- Prefer the standard library. Adding a dependency needs a clear reason.
  Current dependencies: pgx/v5 (PostgreSQL), goose/v3 (migrations),
  google/uuid (UUIDv7 IDs), golang.org/x/time/rate (rate limiters),
  github.com/anthropics/anthropic-sdk-go (the Claude provider).

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
5. Implement `sources.Adapter` in the same package and register it in
   `adapters()` in `cmd/synergy/fetch.go`. Follow the contract documented in
   `internal/sources/adapter.go`:
   - honor ctx
   - return `domain.Candidate`s with stable external IDs
   - do no database access and no dedup
   - return partial results together with an error
   - keep credentials out of errors

   Call upstream APIs through `httpx.New`, sharing a limiter from
   `httpx.Limiters` keyed by the source type. Test parsing with recorded
   fixtures and the client with `httptest`; never hit the network in tests
   (a `//go:build live` smoke test is the only exception).
6. Extend `TestAdaptersEndToEnd` in `cmd/synergy` with the new type.

The ingestion pipeline (`internal/ingest`) must not change to add a source.

No database migration is needed: source type and config are open-ended in
the schema and validated in Go.

## Web app rules (`web/`)

- The web app consumes the HTTP API only. No database access, and no data
  logic that duplicates the API (filtering, searching, sorting or paging
  happen in the API). Do not add backend endpoints for the UI without
  approval; record the need in ROADMAP.md.
- Server components fetch through `web/lib/api.ts`. Browser requests go to
  `/api/v1/*` on the web app, which proxies to the API (`next.config.ts`).
- Keep dependencies to `next`, `react` and `react-dom` at runtime. Styling
  is plain CSS with the tokens in `app/globals.css`, with light and dark
  values for each token.
- Before committing web changes: `npm run lint`, `npm run typecheck` and
  `npm run build` in `web/`.
- External links use `target="_blank" rel="noopener noreferrer"`.

## Database rules

- Schema changes go in a new numbered file in `migrations/`
  (`NNNNN_description.sql` with `-- +goose Up` / `-- +goose Down`). Never edit
  a migration that has been applied anywhere.
- Every migration must be reversible and pass `TestMigrateDownIsReversible`.
  `TestMigrateUpgradesExistingDatabase` checks upgrades over existing data;
  extend it when a migration transforms data.
- Store methods return domain errors (`domain.ErrNotFound`, `ErrConflict`,
  `ErrInvalid`, `ErrFetchInProgress`, and `ErrUnavailable` when PostgreSQL
  cannot be reached); callers use `errors.Is`. The API maps them to HTTP
  statuses only in `writeServiceError`.
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
