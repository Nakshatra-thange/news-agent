import Link from "next/link";
import { feedHref, type FeedParams, type Item } from "@/lib/api";
import { absoluteTime, authorLine, hostname, itemStats, kindLabel, relativeTime, sourceTypeLabel } from "@/lib/format";
import { ItemSummary } from "./ItemSummary";
import { SourceBadge } from "./SourceBadge";

const maxCardTags = 3;

/** One feed entry as a flashcard: the headline on the front, the summary
 *  and links on the back. Flipping is a visually hidden checkbox styled by
 *  CSS, so it needs no hooks: rendered on the server for the first page and
 *  on the client for pages loaded with "Load more". */
export function ItemCard({ item, params }: { item: Item; params: FeedParams }) {
  const stats = itemStats(item);
  const showDiscussion = item.discussion_url !== "" && item.discussion_url !== item.url;
  const sightings = item.also_seen_on;
  const flipId = `flip-${item.id}`;

  return (
    <article className="flash" data-source={item.source.type}>
      <input type="checkbox" id={flipId} className="flip-toggle" aria-label={`Flip card: ${item.title}`} />
      <div className="flash-inner">
        <div className="flash-face flash-front">
          <div className="flash-top">
            <div className="card-kicker">
              <SourceBadge type={item.source.type} />
              <span className="kind">{kindLabel(item.kind)}</span>
            </div>
            <h2 className="flash-title">
              <Link href={`/items/${item.id}`}>{item.title}</Link>
            </h2>
            {item.source.type === "arxiv" && item.authors.length > 0 && (
              <p className="card-authors">By {authorLine(item.authors, 2)}</p>
            )}
          </div>
          <div className="flash-strip">
            <div className="strip-text">
              <time dateTime={item.feed_at} title={absoluteTime(item.feed_at)} className="strip-date">
                {item.published_at ? "" : "found "}
                {relativeTime(item.feed_at)}
              </time>
              <span className="strip-meta">
                {[hostname(item.url), ...stats].join(" · ")}
                {sightings.length > 0 &&
                  ` · also on ${[...new Set(sightings.map((s) => sourceTypeLabel(s.source.type)))].join(", ")}`}
              </span>
            </div>
            <label htmlFor={flipId} className="flip-btn" aria-hidden="true" title="Flip for the gist">
              <FlipIcon />
            </label>
          </div>
        </div>

        <div className="flash-face flash-back">
          <div className="back-head">
            <span className="back-label">The gist</span>
            <label htmlFor={flipId} className="flip-btn" aria-hidden="true" title="Flip back">
              <FlipIcon />
            </label>
          </div>
          <div className="back-body">
            <ItemSummary item={item} variant="card" />
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
              {item.tags.length > maxCardTags && <li className="more">+{item.tags.length - maxCardTags}</li>}
            </ul>
          )}
          <div className="back-links">
            <Link href={`/items/${item.id}`} className="ext strong">
              Details
            </Link>
            <a className="ext" href={item.url} target="_blank" rel="noopener noreferrer">
              {hostname(item.url)} <span aria-hidden="true">↗</span>
            </a>
            {showDiscussion && (
              <a className="ext" href={item.discussion_url} target="_blank" rel="noopener noreferrer">
                discussion <span aria-hidden="true">↗</span>
              </a>
            )}
          </div>
        </div>
      </div>
    </article>
  );
}

function FlipIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
      <path
        d="M4 12a8 8 0 0 1 13.7-5.6M20 12a8 8 0 0 1-13.7 5.6"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.4"
        strokeLinecap="round"
      />
      <path d="M18.5 2.5v4.2h-4.2M5.5 21.5v-4.2h4.2" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
