import Link from "next/link";
import { feedHref, type FeedParams } from "@/lib/api";

/** Latest (newest first) or For you (ranked by importance, recency and the
 *  saved preferences). Switching keeps every filter. */
export function ViewNav({ params }: { params: FeedParams }) {
  const views = [
    { view: undefined, label: "Latest" },
    { view: "foryou" as const, label: "For you" },
  ];
  return (
    <nav className="view-nav" aria-label="Feed order">
      {views.map((v) => (
        <Link
          key={v.label}
          href={feedHref({ ...params, view: v.view })}
          className="tab"
          aria-current={params.view === v.view ? "page" : undefined}
        >
          {v.label}
        </Link>
      ))}
      {params.view === "foryou" && (
        <Link href="/preferences" className="header-link">
          Edit preferences
        </Link>
      )}
    </nav>
  );
}
