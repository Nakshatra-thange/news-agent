import Link from "next/link";
import { feedHref, type FeedParams, type Item } from "@/lib/api";
import { absoluteTime, authorLine, hostname, itemStats, kindLabel, relativeTime, sourceTypeLabel } from "@/lib/format";
import { SourceBadge } from "./SourceBadge";

const maxCardTags = 4;

/** One feed entry. No hooks: rendered on the server for the first page and
 *  on the client for pages loaded with "Load more". */
export function ItemCard({ item, params }: { item: Item; params: FeedParams }) {
  const stats = itemStats(item);
  const showDiscussion = item.discussion_url !== "" && item.discussion_url !== item.url;
  const sightings = item.also_seen_on;

  return (
    <article className="card">
      <div className="card-meta">
        <SourceBadge type={item.source.type} />
        <span className="muted">{kindLabel(item.kind)}</span>
        <span aria-hidden="true" className="dot">·</span>
        <time dateTime={item.feed_at} title={absoluteTime(item.feed_at)} className="muted">
          {item.published_at ? "" : "found "}
          {relativeTime(item.feed_at)}
        </time>
      </div>

      <h2 className="card-title">
        <Link href={`/items/${item.id}`}>{item.title}</Link>
      </h2>

      {item.source.type === "arxiv" && item.authors.length > 0 && (
        <p className="card-authors muted">{authorLine(item.authors)}</p>
      )}
      {item.description && <p className="card-desc">{item.description}</p>}

      <div className="card-foot">
        <a className="ext" href={item.url} target="_blank" rel="noopener noreferrer">
          {hostname(item.url)} <span aria-hidden="true">↗</span>
        </a>
        {showDiscussion && (
          <a className="ext muted" href={item.discussion_url} target="_blank" rel="noopener noreferrer">
            discussion <span aria-hidden="true">↗</span>
          </a>
        )}
        {stats.map((s) => (
          <span key={s} className="muted">
            {s}
          </span>
        ))}
        {sightings.length > 0 && (
          <span className="seen" title="The same link was also found on these sources">
            also on {[...new Set(sightings.map((s) => sourceTypeLabel(s.source.type)))].join(", ")}
          </span>
        )}
      </div>

      {item.tags.length > 0 && (
        <ul className="tags" aria-label="Tags">
          {item.tags.slice(0, maxCardTags).map((t) => (
            <li key={t}>
              <Link
                className="tag"
                href={feedHref({ ...params, tag: params.tag.includes(t) ? params.tag : [...params.tag, t] })}
                aria-current={params.tag.includes(t) ? "true" : undefined}
              >
                {t}
              </Link>
            </li>
          ))}
          {item.tags.length > maxCardTags && <li className="muted more">+{item.tags.length - maxCardTags}</li>}
        </ul>
      )}
    </article>
  );
}
