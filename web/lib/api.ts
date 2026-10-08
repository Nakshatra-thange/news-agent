// Types and server-side access for the Synergy Go API (Phase 1).
// Field names mirror the API's JSON exactly; see the repository README.

export type SourceType = "hackernews" | "arxiv" | "github";

export type SourceRef = {
  id: string;
  slug: string;
  name: string;
  type: SourceType;
};

export type Sighting = {
  item_id: string;
  source: SourceRef;
  url: string;
  discussion_url: string;
  discovered_at: string;
};

export type Item = {
  id: string;
  source: SourceRef;
  external_id: string;
  kind: string;
  title: string;
  description: string;
  url: string;
  canonical_url: string;
  discussion_url: string;
  authors: string[];
  tags: string[];
  metadata: Record<string, unknown>;
  published_at: string | null;
  discovered_at: string;
  feed_at: string;
  updated_at: string;
  last_seen_at: string;
  duplicate_of: string | null;
  also_seen_on: Sighting[];
};

export type FeedPage = {
  items: Item[];
  count: number;
  has_more: boolean;
  next_cursor: string | null;
};

export type Source = {
  id: string;
  slug: string;
  name: string;
  type: SourceType;
  status: string;
};

export type FieldError = { field: string; message: string };

/** Why a request failed, in terms the UI can explain. */
export type ApiError = {
  kind: "network" | "unavailable" | "invalid" | "not_found" | "server";
  message: string;
  details?: FieldError[];
};

export type ApiResult<T> = { ok: true; data: T } | { ok: false; error: ApiError };

const apiURL = process.env.SYNERGY_API_URL ?? "http://127.0.0.1:8080";
const requestTimeoutMs = 8000;

/** Fetches a JSON resource from the Go API on the server, never throwing. */
async function getJSON<T>(path: string): Promise<ApiResult<T>> {
  let res: Response;
  try {
    res = await fetch(`${apiURL}${path}`, {
      cache: "no-store",
      signal: AbortSignal.timeout(requestTimeoutMs),
    });
  } catch {
    return { ok: false, error: { kind: "network", message: "The Synergy API could not be reached." } };
  }
  if (res.ok) {
    return { ok: true, data: (await res.json()) as T };
  }
  return { ok: false, error: await toApiError(res) };
}

/** Maps an error response ({"error": {code, message, details}}) to an ApiError. */
export async function toApiError(res: Response): Promise<ApiError> {
  let body: { error?: { code?: string; message?: string; details?: FieldError[] } } = {};
  try {
    body = await res.json();
  } catch {
    // Not the API's JSON error format. A 5xx then comes from the /api/v1
    // proxy in front of an API that is down, not from the API itself.
    if (res.status >= 500) {
      return { kind: "network", message: "The Synergy API could not be reached." };
    }
  }
  const message = body.error?.message ?? `Request failed (HTTP ${res.status}).`;
  switch (res.status) {
    case 400:
      return { kind: "invalid", message, details: body.error?.details };
    case 404:
      return { kind: "not_found", message };
    case 502:
    case 503:
    case 504:
      return { kind: "unavailable", message };
    default:
      return { kind: "server", message };
  }
}

export function getFeed(query: URLSearchParams): Promise<ApiResult<FeedPage>> {
  return getJSON<FeedPage>(`/api/v1/items?${query}`);
}

export function getItem(id: string): Promise<ApiResult<Item>> {
  return getJSON<Item>(`/api/v1/items/${encodeURIComponent(id)}`);
}

export async function getActiveSources(): Promise<ApiResult<Source[]>> {
  const res = await getJSON<{ sources: Source[] }>("/api/v1/sources?status=active");
  return res.ok ? { ok: true, data: res.data.sources } : res;
}

// --- Feed query ------------------------------------------------------------

/** The feed's own URL parameters. They mirror the API's names, except
 *  `range`, a relative time window turned into an absolute `since`. */
export type FeedParams = {
  q?: string;
  source?: string;
  source_type?: string;
  kind?: string;
  tag: string[];
  range?: string;
};

export const timeRanges: Record<string, { label: string; hours: number }> = {
  "24h": { label: "Past 24 hours", hours: 24 },
  "7d": { label: "Past week", hours: 24 * 7 },
  "30d": { label: "Past month", hours: 24 * 30 },
};

export const pageSize = 30;

type RawParams = Record<string, string | string[] | undefined>;

function first(v: string | string[] | undefined): string | undefined {
  const s = Array.isArray(v) ? v[0] : v;
  return s?.trim() || undefined;
}

export function parseFeedParams(raw: RawParams): FeedParams {
  const tags = raw.tag === undefined ? [] : Array.isArray(raw.tag) ? raw.tag : [raw.tag];
  return {
    q: first(raw.q),
    source: first(raw.source),
    source_type: first(raw.source_type),
    kind: first(raw.kind),
    tag: [...new Set(tags.map((t) => t.trim()).filter(Boolean))],
    range: first(raw.range),
  };
}

/** Builds the API query for the first page. The API validates the values;
 *  invalid ones come back as a 400 the page explains. */
export function apiQuery(p: FeedParams, now: Date): URLSearchParams {
  const q = new URLSearchParams({ limit: String(pageSize) });
  if (p.q) q.set("q", p.q);
  if (p.source) q.set("source", p.source);
  if (p.source_type) q.set("source_type", p.source_type);
  if (p.kind) q.set("kind", p.kind);
  for (const t of p.tag) q.append("tag", t);
  const range = p.range ? timeRanges[p.range] : undefined;
  if (range) {
    // Whole minutes, so the value is stable within a page load.
    const since = new Date(now.getTime() - range.hours * 3600_000);
    since.setUTCSeconds(0, 0);
    q.set("since", since.toISOString());
  }
  return q;
}

/** Builds a feed URL from params, dropping empty values. */
export function feedHref(p: Partial<FeedParams>): string {
  const q = new URLSearchParams();
  if (p.q) q.set("q", p.q);
  if (p.source_type) q.set("source_type", p.source_type);
  if (p.source) q.set("source", p.source);
  if (p.kind) q.set("kind", p.kind);
  for (const t of p.tag ?? []) q.append("tag", t);
  if (p.range) q.set("range", p.range);
  const s = q.toString();
  return s ? `/?${s}` : "/";
}
