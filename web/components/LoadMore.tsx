"use client";

import { useState } from "react";
import { toApiError, type FeedPage, type FeedParams, type Item } from "@/lib/api";
import { ItemCard } from "./ItemCard";

type Props = {
  /** The exact API query of the first page. The API binds cursors to their
   *  filters, so later pages must repeat it unchanged, plus the cursor. */
  query: string;
  initialCursor: string | null;
  params: FeedParams;
};

/** Appends further feed pages using the API's keyset cursor. Requests go to
 *  /api/v1 on this app, which proxies to the Go API. */
export function LoadMore({ query, initialCursor, params }: Props) {
  const [items, setItems] = useState<Item[]>([]);
  const [cursor, setCursor] = useState(initialCursor);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function loadNext() {
    if (!cursor || loading) return;
    setLoading(true);
    setError(null);
    try {
      const qs = new URLSearchParams(query);
      qs.set("cursor", cursor);
      const res = await fetch(`/api/v1/items?${qs}`, { cache: "no-store" });
      if (!res.ok) {
        const err = await toApiError(res);
        setError(
          err.kind === "network" || err.kind === "unavailable"
            ? "The Synergy API isn’t responding right now."
            : err.message,
        );
        return;
      }
      const page = (await res.json()) as FeedPage;
      setItems((prev) => [...prev, ...page.items]);
      setCursor(page.has_more ? page.next_cursor : null);
    } catch {
      setError("Network error: check your connection and the Synergy API.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <>
      {items.length > 0 && (
        <ol className="feed">
          {items.map((item) => (
            <li key={item.id}>
              <ItemCard item={item} params={params} />
            </li>
          ))}
        </ol>
      )}
      <div className="feed-end">
        {error && (
          <p className="inline-error" role="alert">
            Couldn’t load more items. {error}
          </p>
        )}
        {cursor ? (
          <button type="button" className="button" onClick={loadNext} disabled={loading} aria-busy={loading}>
            {loading ? "Loading…" : error ? "Try again" : "Load more"}
          </button>
        ) : (
          <p className="muted">You’re all caught up.</p>
        )}
      </div>
    </>
  );
}
