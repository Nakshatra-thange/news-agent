"use client";

import { useRouter } from "next/navigation";
import { useRef, useState, useTransition } from "react";
import { feedHref, timeRanges, type FeedParams, type Source } from "@/lib/api";
import { kindLabels } from "@/lib/format";

type Props = {
  params: FeedParams;
  /** Active sources for the source picker; null if they could not be loaded. */
  sources: Source[] | null;
};

/** Search box plus a few compact filters. The URL is the only state: every
 *  change navigates, and the server renders the matching feed. */
export function FilterBar({ params, sources }: Props) {
  const router = useRouter();
  const form = useRef<HTMLFormElement>(null);
  const [pending, startTransition] = useTransition();
  const [open, setOpen] = useState(false);

  const pickable = (sources ?? []).filter((s) => !params.source_type || s.type === params.source_type);
  const activeFilters = [params.source, params.kind, params.range].filter(Boolean).length;

  function apply() {
    const data = new FormData(form.current!);
    const value = (k: string) => (data.get(k) as string | null)?.trim() || undefined;
    const href = feedHref({
      q: value("q"),
      source_type: params.source_type,
      source: value("source"),
      kind: value("kind"),
      range: value("range"),
      tag: params.tag,
    });
    startTransition(() => router.push(href));
  }

  return (
    <form
      ref={form}
      role="search"
      className="filters"
      data-pending={pending || undefined}
      onSubmit={(e) => {
        e.preventDefault();
        apply();
      }}
    >
      <div className="search-row">
        <input
          className="search"
          type="search"
          name="q"
          defaultValue={params.q ?? ""}
          key={params.q ?? ""}
          placeholder="Search titles and descriptions"
          aria-label="Search"
          enterKeyHint="search"
        />
        <button
          type="button"
          className="button filters-toggle"
          aria-expanded={open}
          aria-controls="filter-panel"
          onClick={() => setOpen((o) => !o)}
        >
          Filters{activeFilters > 0 ? ` · ${activeFilters}` : ""}
        </button>
      </div>

      <div id="filter-panel" className="filter-panel" data-open={open}>
        {pickable.length > 1 && (
          <select name="source" aria-label="Source" defaultValue={params.source ?? ""} key={`s-${params.source}`} onChange={apply}>
            <option value="">All sources</option>
            {pickable.map((s) => (
              <option key={s.id} value={s.slug}>
                {s.name}
              </option>
            ))}
          </select>
        )}
        <select name="kind" aria-label="Kind" defaultValue={params.kind ?? ""} key={`k-${params.kind}`} onChange={apply}>
          <option value="">All kinds</option>
          {Object.entries(kindLabels).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
        <select name="range" aria-label="Time range" defaultValue={params.range ?? ""} key={`r-${params.range}`} onChange={apply}>
          <option value="">Any time</option>
          {Object.entries(timeRanges).map(([value, r]) => (
            <option key={value} value={value}>
              {r.label}
            </option>
          ))}
        </select>
        <span className="pending muted" aria-live="polite">
          {pending ? "Updating…" : ""}
        </span>
      </div>
    </form>
  );
}
