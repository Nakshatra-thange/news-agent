import Link from "next/link";
import { feedHref, type FeedParams } from "@/lib/api";
import { sourceTypeLabels } from "@/lib/format";

const tabs: { type?: string; label: string }[] = [
  { label: "All" },
  ...Object.entries(sourceTypeLabels).map(([type, label]) => ({ type, label })),
];

/** All / Hacker News / arXiv / GitHub, via the API's source_type filter.
 *  Switching keeps the search, kind, time and tags but drops a specific
 *  source, which may belong to another type. */
export function SourceNav({ params }: { params: FeedParams }) {
  return (
    <nav className="source-nav" aria-label="Sources">
      {tabs.map((t) => {
        const active = (params.source_type ?? undefined) === t.type;
        return (
          <Link
            key={t.label}
            href={feedHref({ ...params, source_type: t.type, source: undefined })}
            className="tab"
            data-source={t.type}
            aria-current={active ? "page" : undefined}
          >
            {t.label}
          </Link>
        );
      })}
    </nav>
  );
}
