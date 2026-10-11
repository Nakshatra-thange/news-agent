# Synergy

Synergy is a personal, locally run feed of AI developments. It collects
items from Hacker News, arXiv and GitHub and presents them in a web app,
either newest first or ranked "for you" by your topic and source
preferences, with links back to the originals.

## Status

**The MVP is complete.** It runs locally with PostgreSQL, the Go API and
the web app, and needs no paid API for its basic features. In short:

- fetch, deduplicate and store items from three sources, automatically or
  on demand
- browse them in the web app (search, filters, item pages), newest first or
  **For you** (ranked, personalized by the preferences you set on the
  **Preferences** page)
- optional AI steps run from the CLI with your own API keys (see [AI
  features and API keys](#ai-features-and-api-keys))

What each phase added:

**Phase 1: core ingestion and feed infrastructure.** Synergy:

- manages its sources through a registry and REST API
- fetches real data from Hacker News, arXiv and GitHub through a
  source-agnostic ingestion pipeline (normalize, canonicalize, deduplicate,
  store, track fetch runs and source health)
- serves the stored items as a paginated, filterable, searchable feed at
  `/api/v1/items`

**Phase 2.1 adds a web app** (`web/`, Next.js): the feed with search,
filters and source navigation, item pages, dark mode and a mobile layout,
built only on the Phase 1 API. See [Web app](#web-app).

Phases 2.2 to 2.8 added the fetch scheduler, AI enrichment and summaries,
embeddings, story clustering, ranking (`sort=ranked`) and explicit
preferences (`personalized=true`, the web app's For you view). Phase 2.9
(feedback) exists in the backend but is switched off in the MVP. Synergy
has no users or authentication and is meant for a single local server.
See `ROADMAP.md` and [Current limitations](#current-limitations).

## Architecture in one paragraph

A single Go binary (`cmd/synergy`) with subcommands:

- `serve` runs the HTTP API and the fetch scheduler.
- `migrate` and `seed` manage the database.
- `fetch` runs ingestion from the CLI.
- `enrich` extracts topics, entities, importance and category from items.
- `summarize` writes short summaries of items with Claude.
- `embed` stores embedding vectors of items (Voyage AI).
- `cluster` groups embedded items into stories.

Source *adapters* turn an upstream API into candidate items. The
source-agnostic *ingest* pipeline normalizes, canonicalizes and deduplicates
them, then stores them in PostgreSQL through the *store* package, the only
code that speaks SQL. The *api* package serves sources, fetch runs and the
feed as JSON. `ARCHITECTURE.md` has the full picture.

## Repository layout

```
cmd/synergy/          entry point: serve, migrate, seed, fetch, version; wiring
internal/config/      environment configuration, validated at startup
internal/domain/      core types (Source, Item, FetchRun), validation, sentinel errors
internal/canon/       URL canonicalization and text normalization
internal/sources/     source registry, type specs, adapter contract
  hackernews/ arxiv/ github/   one package per source type (config + adapter)
internal/httpx/       polite HTTP client: rate limits, retries, redaction
internal/ingest/      fetch pipeline: normalize, dedup, store, run bookkeeping
internal/scheduler/   automatic fetching inside `serve`
internal/enrich/      LLM enrichment and summaries: provider interface, Claude provider, prompts, validation
internal/embed/       item embeddings: provider interface, Voyage AI provider, fake provider
internal/cluster/     story clustering over stored embeddings
internal/store/       PostgreSQL access (pgx) and embedded migrations (goose)
internal/api/         HTTP handlers, middleware, JSON errors
migrations/           numbered SQL migrations, embedded in the binary
scripts/db-setup.sh   one-time local PostgreSQL bootstrap
web/                  Next.js web app over the API (see "Web app")
```

## Requirements

- Go 1.26+. `go.mod` pins `toolchain go1.26.6`, which carries standard-library
  security fixes. With the default `GOTOOLCHAIN=auto`, an older local Go
  downloads it automatically on first use.
- PostgreSQL 17 running locally (no Docker needed), with superuser access
  once for `make db-setup`
- `psql` and `openssl` on `PATH` (used only by `make db-setup`)
- `curl` (and optionally `jq`) to explore the API
- Node.js 20.9+ and npm, only for the web app

## Quick start

From a fresh clone:

```sh
make db-setup            # one-time: creates role + databases, writes .env
make migrate             # applies migrations to the synergy database
make seed                # registers the default Hacker News, arXiv and GitHub sources
make build               # builds bin/synergy
./bin/synergy fetch --all   # fetch every active source once (takes a minute or two)
make run                 # starts the server on 127.0.0.1:8080 (Ctrl-C to stop)
```

In another terminal:

```sh
curl localhost:8080/health/db
# {"status":"ok","schema_version":10,"latest_version":10,"latency_ms":1}
curl localhost:8080/api/v1/sources
curl 'localhost:8080/api/v1/items?limit=5'
```

Then start the web app in a third terminal and open http://localhost:3000:

```sh
cd web
npm ci
npm run build && npm start
```

While `make run` is up, the scheduler keeps fetching sources once their
interval has passed. Use `SCHEDULER_ENABLED=false make run` to serve only
what is already stored.

The `make` targets read `.env`. To run `./bin/synergy` directly, export the
file first: `set -a; . ./.env; set +a`. No API keys are needed for any of
the above.

## Web app

`web/` is a Next.js (App Router, React, TypeScript) app over the API. It
has no database access, no data logic of its own and no UI library: plain
CSS, three runtime dependencies (`next`, `react`, `react-dom`).

With the API running (`make run`):

```sh
cd web
npm ci
npm run dev                  # development, http://localhost:3000
# or, for production:
npm run build && npm start   # http://localhost:3000
```

`SYNERGY_API_URL` (default `http://127.0.0.1:8080`) points it at the API. It
is read when building and when starting.

**Pages:**

- **`/`, the feed.**
  - **Latest / For you.** Latest is newest first. For you (`?view=foryou`)
    requests `sort=ranked&personalized=true`: items matching your
    preferred topics or sources move up (see Ranking in the API section),
    and a link leads to the preferences. Every filter below works in both
    views.
  - Search box: the API's full-text `q`.
  - Source tabs: All, Hacker News, arXiv, GitHub (`source_type`).
  - Pickers for source, kind and time range (past day, week or month,
    sent as `since`).
  - Clicking a tag filters by it; tag chips remove it.
  - Each card shows the stored summary (clamped to five lines) or, when
    there is none, the source's description labelled "From the source".
  - "Load more" follows the API's keyset cursor.
  - The URL holds all feed state, so every view can be bookmarked and
    shared.
- **`/items/{id}`, one item.**
  - "The brief": the full summary with its model and age, or a clear
    note that none exists yet.
  - Everything the API knows about the item: authors, full description,
    tags, dates, source-specific facts (stars, points, category, ...),
    and other sources that carried the same link.
  - Buttons open the original, the arXiv PDF and the HN discussion.
  - Unknown or malformed IDs get a 404 page.
- **`/preferences`** (linked in the header). Topics (comma-separated,
  matched against item tags such as `cs.LG` or `llm`) and sources of
  interest for the For you view, saved with `PUT /api/v1/preferences`.

**How it talks to the API:**

- The pages render on the server and call the API directly.
- In the browser, only "Load more" calls the API. Its requests go to
  `/api/v1/*` on the web app, which proxies them to `SYNERGY_API_URL`
  (`next.config.ts`), so the API needs no CORS.
- Later pages repeat the first page's exact API query plus the cursor,
  because the API binds cursors to their filters.

**Behavior:**

- **States:** loading skeletons; empty, no-results and invalid-filter
  messages; API-unreachable and database-unavailable messages with a retry.
  A failed "Load more" shows an inline retry and keeps the loaded items.
- **Design:** a vintage editorial look. Parchment backgrounds with a light
  CSS-only paper grain, navy ink accents, Playfair Display headlines,
  Source Serif 4 summaries and Inter for controls (self-hosted by
  `next/font` at build time). No article photos. The masthead emblem is
  the brand artwork with a muted green treatment, in `web/public/brand/`
  (favicon: `web/app/icon.png`). On wide screens the first story leads
  across a two-column grid.
- **Theme:** follows the system light or dark preference; the header
  toggle overrides it and is remembered.
- **Mobile:** the source tabs scroll sideways and the filters fold behind a
  "Filters" button.

**Checks:**

```sh
cd web
npm run lint        # ESLint (next/core-web-vitals + TypeScript rules)
npm run typecheck   # route types + tsc --noEmit
npm run build       # production build
```

`go.mod` ignores `web/node_modules`, and the Makefile's gofmt targets cover
only Go packages, so Go tooling never touches npm packages.

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
| `hackernews` | `queries` (8 AI keywords, 1-20), `min_points` (30), `lookback` ("2d", 1h-30d), `lists` (["top","best"]; also `new`, `ask`, `show`), `max_items` (300, max 1000) | 5m |
| `arxiv` | `categories` (["cs.AI","cs.LG","cs.CL"], 1-20), `max_results` (100, max 500), `lookback` ("3d", 1h-30d) | 30m |
| `github` | `queries` (AI topics, 1-10; no `created:`/`stars:`/`sort:` qualifiers), `created_within` ("7d", 1d-365d), `min_stars` (50), `sort` ("stars" or "updated"), `max_results_per_query` (30, max 100) | 10m |

The minimum interval is a politeness floor: no source can be configured to
fetch its upstream API more often than that.

## Ingestion

A fetch of one source runs this pipeline (`internal/ingest`):

1. **Pre-flight.** The source must be `active` and its type must have an
   adapter. Unless forced, its `min_fetch_interval` must have passed since the
   last fetch. A *fetch run* is recorded, and the database allows only one
   running run per source.
2. **Fetch.** The type's adapter returns candidate items. The fetch is bounded
   by `FETCH_TIMEOUT` and cancellable. A panicking adapter becomes a failed
   run, not a crash.
3. **Normalize and validate** each candidate:
   - Text is cleaned (whitespace collapsed; control characters, NUL and
     invalid UTF-8 removed) and bounded.
   - Authors and tags are deduplicated.
   - Implausible dates (before 1990, or more than 24h in the future) are
     dropped.
   - Metadata is sanitized for PostgreSQL.
   - Candidates that cannot be repaired (no title, unusable URL, unknown kind)
     are rejected and counted. Repeats of an external ID within one fetch are
     folded.
   - **English only.** Clearly non-English candidates are rejected the same
     way (counted in `rejected`, logged as `not English: ...`). See
     "Language filtering" below.
4. **Canonicalize and hash** (`internal/canon`):
   - The URL becomes a canonical identity key: https, lowercase host without
     `www.`, no fragment or default port, cleaned path, tracking parameters
     (`utm_*`, `fbclid`, `gclid`, `ref`, ...) removed and the query sorted.
   - arXiv `abs`/`pdf`/`html` links collapse to `https://arxiv.org/abs/<id>`
     without a version.
   - GitHub repository and code-browsing links collapse to
     `https://github.com/<owner>/<repo>`. Issues, PRs and releases stay
     distinct.
   - A content hash covers the normalized title and description.
5. **Deduplicate and store** in one transaction (all items or none):
   - **Same source and external ID** is the same item. It is updated only if a
     stored field changed (content hash, metadata, tags, ...); otherwise only
     `last_seen_at` moves.
   - **Same canonical URL from another source**: the item is stored and linked
     with `duplicate_of` to the first item, so provenance is never lost.
     Linked duplicates are hidden from the default feed.
6. **Finish.** The run records its counts (fetched, inserted, updated,
   unchanged, duplicate, rejected) and any error. In the same transaction the
   source's health is updated: a success resets the failure streak and saves
   the adapter's cursor state; a failure records the error and extends the
   streak.

Candidates from a partially failed fetch are still stored, but the run is
marked failed and the cursor state is not advanced. A run left `running` by a
crashed or killed process is marked failed when `synergy serve` or
`synergy fetch` next starts, and counts as a failure in its source's health.
Only runs older than any live fetch could be are touched (`FETCH_TIMEOUT`
plus a margin), so a fetch running in another process is never disturbed.

Fetches stop cleanly:

- **Ctrl-C / SIGTERM.** The CLI cancels its fetch. The server stops accepting
  requests, waits up to `SHUTDOWN_TIMEOUT` for in-flight fetches, then
  cancels them.
- **`FETCH_TIMEOUT`.** A fetch that runs too long is aborted.

Either way, items already fetched are stored and the run is recorded as
`failed` with the reason, for example `fetch aborted: timed out after 2m`.

Semantic or story-level deduplication (the same news at different URLs) is
deliberately out of scope until the clustering phase.

### Language filtering

Synergy shows English content only. `internal/ingest/language.go` checks
the title plus the first 1,500 characters of the description, offline and
without dependencies, and rejects a candidate only on strong evidence:

- **Script.** Most letters are in a non-Latin script (Chinese, Japanese,
  Korean, Cyrillic, Arabic, ...), with at least 4 such letters. Each CJK
  character counts as two letters, so a Latin repository slug or product
  name cannot outweigh a description written in Chinese.
- **Function words.** In Latin-script text, at least 3 distinctive Spanish,
  French, German, Portuguese, Italian, Dutch or Swedish function words
  ("und", "pour", "para", ...) and more than twice as many as English ones
  ("the", "and", "for", ...).

Everything else is kept: short titles, version strings, English text with
a foreign name or a few CJK characters, bilingual descriptions, and text
that cannot be classified. Discarding English stories is considered worse
than letting an occasional foreign one through. The filter applies to new
fetches; items stored before it existed are not removed.

### Triggering fetches

**Automatically.** While `synergy serve` runs, the scheduler checks every
`SCHEDULER_INTERVAL` (default 1 minute):

- It starts a fetch for each active source whose `min_fetch_interval` has
  passed since its last completed run, successful or failed. Never-fetched
  sources start right away.
- Runs are recorded with trigger `scheduler`.
- A source that is already being fetched, by the scheduler, the CLI or the
  API, is skipped until that fetch ends.
- A failing source is retried at its next interval and never holds up the
  others.
- Each tick also recovers abandoned runs.
- Set `SCHEDULER_ENABLED=false` to fetch only on demand.

**On demand:**

```sh
synergy seed                        # register the default sources (idempotent)
synergy fetch hn-ai                 # one source, synchronously, with a summary line
synergy fetch arxiv-ai
synergy fetch github-ai-repos
synergy fetch hn-ai arxiv-ai        # several, in order
synergy fetch --all                 # every active source by priority
synergy fetch --all --force         # ignore cooldowns

curl -X POST localhost:8080/api/v1/sources/hn-ai/fetch          # async: 202 + run
curl localhost:8080/api/v1/fetch-runs/<run-id>                   # poll the outcome
```

### Source adapters

| Type | Upstream | What one fetch does | Cursor |
|---|---|---|---|
| `hackernews` | Official Firebase API (`hacker-news.firebaseio.com/v0`) | Reads the configured ranked lists, fetches up to `max_items` stories (4 workers, ~10 req/s), keeps stories (not comments, jobs, polls, deleted or dead items) whose title starts a word with a keyword, with enough points, inside `lookback`. Link posts point at their target; text posts at their HN page. The HN discussion URL is always kept. | `old_id_floor`: IDs are assigned in submission order, so IDs at or below the newest too-old story are skipped without a request. |
| `arxiv` | Atom API (`export.arxiv.org/api/query`) | One query over the categories, restricted server-side to `submittedDate` in the lookback window, newest first, paged up to `max_results`, one request per 3 s. Query-error feeds and arXiv's occasional empty pages are detected. Items use the versionless arXiv ID and `https://arxiv.org/abs/<id>`. | None: submission order is not announcement order, so the window plus deduplication is the safe choice. |
| `github` | REST search (`api.github.com/search/repositories`) | One request per query with `created:>=` and `stars:>=` added from config. Repos found by several queries are returned once. Stars, forks, language, license and dates go to metadata, so changes are updates. | None: star counts change and are re-read. |

GitHub authentication is optional. Without `GITHUB_TOKEN`, searches are
spaced 6 s apart (10/min limit); with it, 2 s (30/min). The token is sent
only in the `Authorization` header and never appears in errors or logs. A
rejected token fails the fetch with a hint rather than silently falling back.
GitHub rate-limit responses (403/429 with `X-RateLimit-Remaining: 0`, or
`Retry-After`) wait for the reset when it is within about a minute, otherwise
the fetch fails fast and remaining queries are not attempted.

A fetch that partly fails (one HN item, one GitHub query) stores what it got,
is recorded as failed, and does not advance the cursor.

### Polite upstream access

Adapters call upstream APIs through `internal/httpx`:
- **Rate limiting:** one token-bucket limiter per upstream, shared by all
  sources of that type, applied to every attempt including retries.
- **Retries:** only transient failures are retried: network errors (not
  certificate or unknown-host errors), 408, 429, 502, 503 and 504. Attempts
  are bounded (default 3) with jittered exponential backoff, and
  `Retry-After` is honored.
- **Fail fast:** a `Retry-After` longer than a minute ends the attempt
  instead of stalling the fetch.
- **Limits and redaction:** per-attempt timeouts and a response size cap.
  Errors never include request headers or query strings, where credentials
  live.

## Enrichment

`synergy enrich` extracts structured intelligence from stored items with an
LLM and stores it in `item_enrichments`, one row per item, without touching
the items:

- topics
- entities (name and type)
- importance (1 to 5)
- category (`research`, `model_release`, `tool`, `product_news`,
  `industry`, `tutorial`, `opinion`, `other`)

```sh
synergy enrich --fake               # enrich the 10 newest items that need it
synergy enrich --fake --limit 50    # up to 100 per run
```

- **Bounded and explicit.** Nothing is enriched automatically. Each run
  takes the newest items that lack a current enrichment.
- **Idempotent.** Repeating a run does nothing. Changed items, or a new
  model or prompt version, are picked up again.
- **Validated.** Model output is parsed strictly and checked against fixed
  bounds before it is stored. Malformed output fails that item only.

No real LLM provider is integrated yet. `--fake` is required and uses a
deterministic offline provider; its results are stored with
`provider=fake`. Adding a real provider is the next step (see
`ROADMAP.md`). No credentials or configuration are needed until then.

## Summaries

`synergy summarize` writes a short news summary (1 to 4 sentences, about
400 and at most 600 characters, up to five lines in the feed) for each of a few
items, using Claude or a model on OpenRouter. It says what happened, the context or cause when the
source states it, and why it matters, using only facts in the item and
attributing claims ("the authors report") rather than presenting them as
confirmed. Summaries are stored in `item_summaries`, one per item, without
touching the items.

```sh
export ANTHROPIC_API_KEY=...       # Claude; or put it in .env; never logged
export OPENROUTER_API_KEY=...      # or OpenRouter (used when no Anthropic key is set)
synergy summarize                  # the 3 newest items without a current summary
synergy summarize --limit 10       # at most 20 per run
synergy summarize --fake           # offline fake provider, no API calls
```

- **Bounded and explicit.** Nothing is summarized automatically. A run
  takes the newest items that lack a current summary.
- **Only items with something to summarize.** Items whose description is
  shorter than 100 characters (a Hacker News headline with no text, a
  one-line repository blurb) are never sent to a model: from a headline
  alone, models invent context. The web app says the source gives only a
  headline and links to it.
- **Idempotent.** Repeating a run does nothing. An item whose content
  changed, or a new model (`ANTHROPIC_MODEL`) or prompt version, gets a new
  summary.
- **Validated.** The reply must be a single plain-prose paragraph within
  the length bounds. A reply over 600 characters is first cut back to its
  last whole sentence that fits; nothing is cut mid-sentence. Anything else, including a refusal or a provider
  error, fails that item only, and its existing summary is kept.
- **Untrusted input.** Item text is passed to the model as delimited,
  untrusted data, and the instructions forbid following anything inside
  it.
- **Rate limits.** The SDK retries transient failures. A rate limit or
  overload (HTTP 429/529) that persists ends the run early; run it again
  later and it picks up where it stopped.
- **Providers.** With `ANTHROPIC_API_KEY`, requests go to Claude
  (`claude-opus-5-5` by default) at low effort, with server-side refusal
  fallbacks enabled. Otherwise, with `OPENROUTER_API_KEY`, they go to
  OpenRouter's chat completions API (`OPENROUTER_MODEL`, default
  `nvidia/nemotron-3.5-lightning:free`, which costs nothing but has
  per-minute and daily request limits), non-streaming, temperature 0.2,
  with model reasoning switched off (it is not needed for a short summary
  and would use up the token budget). A model that cannot run without
  reasoning needs a code change. A short `Retry-After` on a 429 is waited
  out, and a 429 without one is retried after 3 s and 8 s (failed requests
  do not use the free daily quota); otherwise 429 and 503 end the run like
  Claude's rate limits. Each item costs one request either way.
- **Display.** The item endpoints return the stored summary as
  `summary` (`{text, provider, model, generated_at}`, or `null`). Only
  summaries from a real model made from the item's current content are
  returned; fake and outdated ones are not. Nothing is generated when a
  page is viewed. Without a summary, the web app labels the source's own
  description as "From the source" and keeps the original link.

To fill the feed with summaries, set `ANTHROPIC_API_KEY` or
`OPENROUTER_API_KEY` in `.env` and run
`synergy summarize --limit 20` as often as needed (newest items first).
Browsing never needs a key.

## Embeddings

`synergy embed` stores an embedding vector for each of a few items, for
later story clustering. Vectors are stored in `item_embeddings`, one per
item and model, without touching the items.

```sh
export VOYAGE_API_KEY=...          # or put it in .env; never logged
synergy embed                      # the 10 newest items without a current embedding
synergy embed --limit 50           # at most 50 per run
synergy embed --fake               # offline fake provider, no API calls
```

- **Input.** The item's title and description (truncated to 8,000
  characters). An item whose title or description changes is re-embedded.
- **Provider.** Voyage AI, model `VOYAGE_MODEL` (default `voyage-3.5`) at
  1024 dimensions; one request per item. `--fake` uses a deterministic
  word-hashing provider whose vectors reflect shared words, not meaning.
- **Validated.** A vector of the wrong length, with non-finite values or
  all zeros fails that item only, and its existing embedding is kept.
- **Bounded and explicit.** Nothing is embedded automatically.
- **Storage.** Vectors are PostgreSQL `real[]` arrays; pgvector is not
  required. Cosine similarity is computed in Go.

## Stories

`synergy cluster` groups embedded items about the same story. It reads
stored embeddings only; it calls no API and never modifies items.

```sh
synergy embed --limit 20           # embed first; clustering never invents vectors
synergy cluster                    # the 20 oldest embedded items without a story
synergy cluster --limit 200        # at most 200 per run
synergy cluster --threshold 0.85   # stricter grouping (default 0.80)
synergy cluster --fake             # cluster the fake provider's embeddings
```

- **How.** Each item joins the story whose first item is most similar
  (cosine similarity at least the threshold), or starts a new story.
- **Per model.** Stories are kept per embedding model (`VOYAGE_MODEL`, or
  the fake model with `--fake`); vectors of different models are never
  compared.
- **Idempotent.** Clustered items are skipped, so repeating a run does
  nothing. Items without a current embedding wait for `synergy embed`.
- **The threshold is a starting point**, not a tuned value; see
  ROADMAP.md.

## Configuration

All configuration is via environment variables (see `.env.example`).

| Variable | Default | Description |
|---|---|---|
| `SERVER_HOST` | `127.0.0.1` | Bind address. Loopback by default: there is no auth. |
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
| `FETCH_TIMEOUT` | `2m` | Upper bound for one fetch of one source, all requests included. |
| `HTTP_USER_AGENT` | `Synergy/0.1 (personal AI research aggregator)` | User-Agent sent upstream. arXiv appreciates a contact address in it. |
| `GITHUB_TOKEN` | (unset) | Optional GitHub token (no scopes needed); raises search limits. Never logged. |
| `SCHEDULER_ENABLED` | `true` | Fetch active sources automatically while `synergy serve` runs. |
| `SCHEDULER_INTERVAL` | `1m` | How often the scheduler checks which sources are due. |
| `ANTHROPIC_API_KEY` | (unset) | Claude API key for `synergy summarize`. Optional; never logged. |
| `ANTHROPIC_MODEL` | `claude-opus-5-5` | Claude model used for summaries. |
| `OPENROUTER_API_KEY` | (unset) | OpenRouter API key for `synergy summarize`, used when `ANTHROPIC_API_KEY` is unset. Optional; never logged. |
| `OPENROUTER_MODEL` | `nvidia/nemotron-3.5-lightning:free` | OpenRouter model used for summaries. |
| `VOYAGE_API_KEY` | (unset) | Voyage AI API key for `synergy embed`. Optional; never logged. |
| `VOYAGE_MODEL` | `voyage-3.5` | Voyage embedding model; must support 1024-dimension output. |

Invalid values stop startup with a message listing every problem.

### AI features and API keys

The app runs without any API key. The AI steps are optional CLI commands,
bounded per run, and never run automatically:

| Command | Needs | Without a key |
|---|---|---|
| `synergy summarize` | `ANTHROPIC_API_KEY` (paid Claude API) or `OPENROUTER_API_KEY` (OpenRouter; free models available) | `--fake` writes offline placeholder summaries |
| `synergy enrich` | nothing: only `--fake` is wired in | the fake provider's results are stored but **ignored by ranking** |
| `synergy embed` | `VOYAGE_API_KEY` (Voyage AI) | `--fake` writes offline word-hash vectors |
| `synergy cluster` | nothing (reads stored embeddings) | works on `--fake` embeddings too |

Put keys in `.env` (git-ignored); they are never logged. Summaries,
enrichments, embeddings and stories are stored but not yet shown in the
web app.

## Development

```sh
make help              # list targets
make test              # all tests (integration tests run if TEST_DATABASE_URL is set)
make test-integration  # all tests, race detector on, failing if TEST_DATABASE_URL is unset
make check             # gofmt, go vet, staticcheck, race-enabled tests
```

Running `go` directly works too, after `set -a; . ./.env; set +a` to enable
the integration tests:

```sh
go vet ./...
go test ./...
go test -race ./...
```

PostgreSQL integration tests live in `internal/store`, `internal/api`,
`internal/ingest` and `cmd/synergy`. They run against `TEST_DATABASE_URL`
and are skipped when it is unset:

- **Isolation.** Each test creates its own schema, migrates it and drops it
  afterwards, so tests leave nothing behind.
- **Safety.** They refuse to run against a database whose name does not end
  in `_test`, so they can never touch your real data.

Adapter tests replay recorded responses from each adapter's `testdata/`
through `httptest` servers; nothing in the normal suite touches the network.
Optional live smoke tests call the real APIs with tiny configurations:

```sh
go test -tags live -count=1 -v ./internal/sources/...
```

## API

The server binds to `127.0.0.1` by default. There is no authentication.

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness probe. Does not touch dependencies. |
| GET | `/health/db` | Pings PostgreSQL and checks the schema version. `200` with `status: ok`; `503` with `status: unavailable` or `migrations_pending`. |
| GET | `/api/v1/source-types` | Supported types with descriptions, default config and fetch policy. |
| GET | `/api/v1/sources` | List sources, highest priority first. `?status=` (repeatable or comma-separated: `active`, `paused`, `retired`, `all`; default active and paused). `?type=`. |
| POST | `/api/v1/sources` | Register a source. Returns `201` with a `Location` header. |
| GET | `/api/v1/sources/{ref}` | Get one source; `{ref}` is its UUID or slug. |
| PATCH | `/api/v1/sources/{ref}` | Partial update of `name`, `url`, `status`, `priority`, `config`, `min_fetch_interval_seconds`. Absent or `null` fields are unchanged. A new `config` replaces the old one. `slug` and `type` are immutable. |
| POST | `/api/v1/sources/{ref}/fetch` | Start an asynchronous fetch. Returns `202` with the run and `Location: /api/v1/fetch-runs/{id}`. `?force=true` ignores the cooldown. |
| GET | `/api/v1/sources/{ref}/runs` | The source's recent fetch runs, newest first. `?limit=` 1-100 (default 20). |
| GET | `/api/v1/fetch-runs/{id}` | One fetch run: `status` (`running`, `succeeded`, `failed`), `stats`, `error`, `duration_ms`. |
| GET | `/api/v1/items` | The feed (see below). |
| GET | `/api/v1/items/{id}` | One item, whatever its source's status or duplicate state. `400 invalid_id` for a malformed UUID, `404` if unknown. |
| GET | `/api/v1/preferences` | The feed preferences: `{"topics": [...], "sources": [{id, slug, name, type}]}`. |
| PUT | `/api/v1/preferences` | Replace them: `{"topics": [...], "sources": [slug or UUID, ...]}` (at most 50 topics, 20 sources). `422` for unknown sources; nothing changes then. |

### Feed

`GET /api/v1/items` returns `{"items": [...], "count": n, "has_more": bool, "next_cursor": string|null}`,
newest first by `feed_at` (`published_at`, or `discovered_at` when the source gives no date), ties broken by
`id`. By default it shows items from **active** sources and hides cross-source duplicates; an item seen
elsewhere lists those sightings in `also_seen_on`. Each item carries its stored `summary`
(`{"text", "provider", "model", "generated_at"}`) or `null` (see Summaries).

| Parameter | Meaning |
|---|---|
| `limit` | 1-200, default 50 (larger is a 400, not silently capped) |
| `cursor` | `next_cursor` from the previous page. Opaque; bound to the filters it was issued for |
| `source` | slugs or UUIDs (repeatable or comma-separated; unknown ones are a 400) |
| `source_type` | `hackernews`, `arxiv`, `github` |
| `source_status` | `active` (default), `paused`, `retired`, `all` |
| `kind` | `paper`, `repository`, `discussion`, `release` |
| `tag` | every given tag must be present |
| `since`, `until` | feed time range `[since, until)`; RFC 3339 or `YYYY-MM-DD` |
| `discovered_since`, `discovered_until` | discovery time range |
| `q` | full-text search over title and description (PostgreSQL web search syntax) |
| `include_duplicates` | `true` to include cross-source duplicates (they carry `duplicate_of`) |
| `sort` | `newest` (default) or `ranked`: feed time shifted 12 hours per enrichment importance point away from 3; see ROADMAP.md (Phase 2.7) |
| `personalized` | `true` (with `sort=ranked` only) also moves items up 12 hours for matching a preferred topic (tag or enrichment topic) and 12 hours for a preferred source; see ROADMAP.md (Phase 2.8) |

Unknown parameters, repeated single-value parameters and malformed query strings are rejected with
`400 invalid_query`. Pagination is keyset-based: pages never repeat or skip items that existed when
paging started; items newer than the cursor appear on the next fresh read.

```sh
curl 'localhost:8080/api/v1/items?limit=10'                         # newest 10
curl 'localhost:8080/api/v1/items?limit=10&cursor=<next_cursor>'    # the next 10
curl 'localhost:8080/api/v1/items?source=arxiv-ai&tag=cs.CL'        # one source, one tag
curl 'localhost:8080/api/v1/items?kind=repository&since=2026-10-01' # repos since a date
curl 'localhost:8080/api/v1/items?q=agent+-survey'                  # full-text search
curl 'localhost:8080/api/v1/items/<id>'                             # one item
```

An item looks like this (abridged):

```json
{
  "id": "01a11668-6afd-797d-a116-a3c467d61212",
  "source": {"id": "01a11070-...", "slug": "arxiv-ai", "name": "arXiv: AI, ML & NLP", "type": "arxiv"},
  "external_id": "2610.08785", "kind": "paper",
  "title": "Conformal Prediction Sets Quantify Information Gain: ...",
  "description": "...", "url": "https://arxiv.org/abs/2610.08785",
  "canonical_url": "https://arxiv.org/abs/2610.08785", "discussion_url": "",
  "authors": ["..."], "tags": ["cs.LG"],
  "metadata": {"pdf_url": "...", "version": "v1", "primary_category": "cs.LG", "updated_at": "..."},
  "published_at": "2026-10-06T17:59:11Z", "discovered_at": "2026-10-07T12:46:20Z",
  "feed_at": "2026-10-06T17:59:11Z", "updated_at": "...", "last_seen_at": "...",
  "duplicate_of": null, "also_seen_on": [], "feedback": null
}
```

`feedback` comes from Phase 2.9. The MVP server cannot set it (the feedback
endpoints are not mounted), so it is `null`.

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
| 409 | `conflict` | Duplicate slug, disallowed status transition, editing a retired source, fetching a paused or retired source, a fetch already running |
| 413 | `payload_too_large` | Body over 1 MiB |
| 415 | `unsupported_media_type` | Content-Type is not `application/json` |
| 422 | `validation_failed` | Field values invalid (all problems listed in `details`) |
| 429 | `too_many_requests` | Source fetched within its `min_fetch_interval` (`Retry-After` header set) |
| 500 | `internal` | Unexpected error; details are logged under the request ID |
| 501 | `not_implemented` | No fetch adapter for the source's type yet |
| 503 | `unavailable` | Database unreachable, or server shutting down. Retry later. `/health` stays up; `/health/db` reports the cause |

Every response carries an `X-Request-ID` header (an incoming one is reused if
well-formed).

## Current limitations

Known limitations of the MVP:

- **The web app edits only preferences.** Managing sources and starting
  fetches are done through the CLI or the API.
- **For you is simple.** It is recency plus fixed boosts: 12 hours for a
  preferred topic match and 12 for a preferred source. Enrichment
  importance would also count, but `synergy enrich` can only use the fake
  provider so far, and ranking ignores fake results. In practice, For you
  is therefore recency plus preferences. Topic matching is exact
  (case-insensitive) against item tags. There is no semantic search, and
  no negative preferences.
- **Feedback is switched off.** The Phase 2.9 "more/less like this" code
  and table exist, but the server does not mount the feedback endpoints
  and ranking ignores feedback.
- **AI output is generated on demand only.**
  - `synergy summarize` (Claude or OpenRouter, when a key is set),
    `synergy enrich` (fake provider only), `synergy embed` and
    `synergy cluster` run explicitly, on a few items at a time.
  - Nothing runs automatically.
  - Summaries are shown in the API's item responses and the web app;
    enrichments, embeddings and stories are not. `q` is plain PostgreSQL
    full-text search (English stemming).
- **Language filtering is heuristic.** It rejects only clearly non-English
  text, so an occasional foreign item can get through, and items stored
  before the filter existed stay in the feed.
- **The scheduler is simple.**
  - It runs inside a single `synergy serve`; don't run several servers
    against one database.
  - Failing sources are retried every interval, without backoff.
- **No users or authentication.** Anyone who can reach the server can manage
  sources and start fetches. It binds to `127.0.0.1` by default and logs a
  warning if bound elsewhere. Do not expose it publicly.
- **Three source types only:** Hacker News, arXiv and GitHub.
- **Exact-URL deduplication in the feed.** The same story at different URLs
  appears more than once. Story clusters (`synergy cluster`) are stored but
  do not merge feed entries.
- **Fetch runs stay inside one process.** An API-triggered fetch runs inside
  the server; if the server is killed, the run is marked failed on the next
  start rather than resumed.
- **Retention.** Items, fetch runs and sources are never deleted.
- **Error detail.** Fetch-run and source `last_error` texts are diagnostic
  and may name upstream URLs (never query strings or credentials).

## Documentation

- `ARCHITECTURE.md`: components, data model, request and fetch flows, design decisions
- `ROADMAP.md`: what Phase 1 delivered and the intended direction for Phase 2
- `AGENTS.md`: rules for contributors and coding agents
