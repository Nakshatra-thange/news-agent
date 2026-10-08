import Link from "next/link";
import { Suspense } from "react";
import { FilterBar } from "@/components/FilterBar";
import { ItemCard } from "@/components/ItemCard";
import { LoadMore } from "@/components/LoadMore";
import { SourceNav } from "@/components/SourceNav";
import { ApiErrorPanel, StatePanel } from "@/components/StatePanel";
import { apiQuery, feedHref, getActiveSources, getFeed, parseFeedParams, type FeedParams } from "@/lib/api";

type SearchParams = Promise<Record<string, string | string[] | undefined>>;

export default async function FeedPage({ searchParams }: { searchParams: SearchParams }) {
  const params = parseFeedParams(await searchParams);
  const sources = await getActiveSources();
  const href = feedHref(params);

  return (
    <>
      <div className="toolbar">
        <SourceNav params={params} />
        <FilterBar params={params} sources={sources.ok ? sources.data : null} />
        {params.tag.length > 0 && (
          <ul className="chips" aria-label="Tag filters">
            {params.tag.map((t) => (
              <li key={t}>
                <Link className="chip" href={feedHref({ ...params, tag: params.tag.filter((x) => x !== t) })}>
                  {t} <span aria-label={`Remove tag ${t}`}>×</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
      {/* Keyed by the filters, so changing them shows the skeleton again. */}
      <Suspense key={href} fallback={<FeedSkeleton />}>
        <FeedResults params={params} href={href} />
      </Suspense>
    </>
  );
}

async function FeedResults({ params, href }: { params: FeedParams; href: string }) {
  const query = apiQuery(params, new Date());
  const result = await getFeed(query);
  if (!result.ok) {
    return <ApiErrorPanel error={result.error} retryHref={href} />;
  }

  const page = result.data;
  const filtered = href !== "/";
  if (page.items.length === 0) {
    return filtered ? (
      <StatePanel title={params.q ? `No results for “${params.q}”` : "Nothing matches these filters"} action={{ href: "/", label: "Clear filters" }}>
        <p>Try fewer filters, a wider time range or different words.</p>
      </StatePanel>
    ) : (
      <StatePanel title="The feed is empty">
        <p>
          No items have been fetched yet. Run <code>synergy fetch --all</code> on the backend, then reload.
        </p>
      </StatePanel>
    );
  }

  return (
    <>
      {params.q && <p className="results-note muted">Results for “{params.q}”, newest first</p>}
      <ol className="feed">
        {page.items.map((item) => (
          <li key={item.id}>
            <ItemCard item={item} params={params} />
          </li>
        ))}
      </ol>
      <LoadMore query={query.toString()} initialCursor={page.has_more ? page.next_cursor : null} params={params} />
    </>
  );
}

function FeedSkeleton() {
  return (
    <ol className="feed" aria-busy="true" aria-label="Loading feed">
      {Array.from({ length: 6 }, (_, i) => (
        <li key={i}>
          <div className="card skeleton">
            <span className="bar w-25" />
            <span className="bar w-80 tall" />
            <span className="bar w-95" />
            <span className="bar w-60" />
          </div>
        </li>
      ))}
    </ol>
  );
}
