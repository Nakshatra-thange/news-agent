"use client";

import { useState } from "react";
import { toApiError, type FeedbackValue } from "@/lib/api";

const labels: Record<FeedbackValue, string> = { more: "More like this", less: "Less like this" };

/** "More like this" / "Less like this" toggles for one item. Clicking the
 *  selected one again removes the feedback. The personalized ranked feed
 *  uses it; nothing is hidden. */
export function FeedbackButtons({ itemId, initial }: { itemId: string; initial: FeedbackValue | null }) {
  const [value, setValue] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function choose(v: FeedbackValue) {
    const next = value === v ? null : v;
    setBusy(true);
    setError(null);
    try {
      const res = await fetch(`/api/v1/items/${encodeURIComponent(itemId)}/feedback`, {
        method: next ? "PUT" : "DELETE",
        headers: next ? { "Content-Type": "application/json" } : undefined,
        body: next ? JSON.stringify({ value: next }) : undefined,
      });
      if (!res.ok) {
        setError((await toApiError(res)).message);
        return;
      }
      setValue(next);
    } catch {
      setError("Network error.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <span className="feedback" role="group" aria-label="Feedback">
      {(Object.keys(labels) as FeedbackValue[]).map((v) => (
        <button
          key={v}
          type="button"
          className="feedback-button"
          aria-pressed={value === v}
          disabled={busy}
          onClick={() => choose(v)}
        >
          {labels[v]}
        </button>
      ))}
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </span>
  );
}
