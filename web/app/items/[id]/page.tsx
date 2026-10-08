import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { cache } from "react";
import { SourceBadge } from "@/components/SourceBadge";
import { ApiErrorPanel } from "@/components/StatePanel";
import { feedHref, getItem, type Item } from "@/lib/api";
import { absoluteTime, compactNumber, hostname, kindLabel, relativeTime, sourceTypeLabel } from "@/lib/format";

type Params = Promise<{ id: string }>;

// One API call per request, shared by the page and its <title>.
const loadItem = cache(getItem);

export async function generateMetadata({ params }: { params: Params }): Promise<Metadata> {
  const res = await loadItem((await params).id);
  return { title: res.ok ? res.data.title : "Item" };
}

export default async function ItemPage({ params }: { params: Params }) {
  const { id } = await params;
  const res = await loadItem(id);
  if (!res.ok) {
    // The API answers 400 for a malformed ID and 404 for an unknown one.
    if (res.error.kind === "not_found" || res.error.kind === "invalid") notFound();
    return <ApiErrorPanel error={res.error} retryHref={`/items/${encodeURIComponent(id)}`} />;
  }
  const item = res.data;
  const facts = itemFacts(item);
  const pdf = typeof item.metadata.pdf_url === "string" ? item.metadata.pdf_url : "";
  const showDiscussion = item.discussion_url !== "" && item.discussion_url !== item.url;

  return (
    <article className="detail">
      <Link href="/" className="back muted">
        ← Feed
      </Link>

      <div className="card-meta">
        <SourceBadge type={item.source.type} />
        <span className="muted">{item.source.name}</span>
        <span aria-hidden="true" className="dot">·</span>
        <span className="muted">{kindLabel(item.kind)}</span>
      </div>

      <h1>{item.title}</h1>

      {item.authors.length > 0 && <p className="authors">{item.authors.join(", ")}</p>}

      <div className="actions">
        <a className="button primary" href={item.url} target="_blank" rel="noopener noreferrer">
          Open on {hostname(item.url)} <span aria-hidden="true">↗</span>
        </a>
        {pdf && (
          <a className="button" href={pdf} target="_blank" rel="noopener noreferrer">
            PDF <span aria-hidden="true">↗</span>
          </a>
        )}
        {showDiscussion && (
          <a className="button" href={item.discussion_url} target="_blank" rel="noopener noreferrer">
            Discussion on {hostname(item.discussion_url)} <span aria-hidden="true">↗</span>
          </a>
        )}
      </div>

      {item.description && <p className="detail-desc">{item.description}</p>}

      {item.tags.length > 0 && (
        <ul className="tags" aria-label="Tags">
          {item.tags.map((t) => (
            <li key={t}>
              <Link className="tag" href={feedHref({ tag: [t] })}>
                {t}
              </Link>
            </li>
          ))}
        </ul>
      )}

      {(item.also_seen_on.length > 0 || item.duplicate_of) && (
        <section className="related">
          <h2>Also seen on</h2>
          <ul>
            {item.duplicate_of && (
              <li>
                First found on another source: <Link href={`/items/${item.duplicate_of}`}>view the original entry</Link>
              </li>
            )}
            {item.also_seen_on.map((s) => (
              <li key={s.item_id}>
                <SourceBadge type={s.source.type} /> <Link href={`/items/${s.item_id}`}>{s.source.name}</Link>
                <span className="muted"> · {relativeTime(s.discovered_at)}</span>
                {s.discussion_url && (
                  <>
                    {" · "}
                    <a href={s.discussion_url} target="_blank" rel="noopener noreferrer">
                      discussion <span aria-hidden="true">↗</span>
                    </a>
                  </>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      <dl className="facts">
        {facts.map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
    </article>
  );
}

function when(iso: string) {
  return (
    <time dateTime={iso} title={absoluteTime(iso)}>
      {absoluteTime(iso)} <span className="muted">({relativeTime(iso)})</span>
    </time>
  );
}

/** The labelled details shown under an item, by source type. */
function itemFacts(item: Item): [string, React.ReactNode][] {
  const m = item.metadata;
  const n = (k: string) => (typeof m[k] === "number" ? compactNumber(m[k] as number) : undefined);
  const s = (k: string) => (typeof m[k] === "string" && m[k] !== "" ? (m[k] as string) : undefined);

  const facts: [string, React.ReactNode | undefined][] = [
    ["Published", item.published_at ? when(item.published_at) : <span className="muted">Not provided by the source</span>],
    ["Discovered", when(item.discovered_at)],
    ["Last seen", when(item.last_seen_at)],
    ["Source", `${item.source.name} (${sourceTypeLabel(item.source.type)})`],
  ];
  switch (item.source.type) {
    case "hackernews":
      facts.push(["Points", n("points")], ["Comments", n("num_comments")]);
      break;
    case "github":
      facts.push(
        ["Stars", n("stars")],
        ["Forks", n("forks")],
        ["Language", s("language")],
        ["License", s("license")],
        ["Last push", s("pushed_at") ? when(s("pushed_at")!) : undefined],
      );
      if (s("homepage")) {
        facts.push([
          "Homepage",
          <a key="homepage" href={s("homepage")} target="_blank" rel="noopener noreferrer">
            {hostname(s("homepage")!)} <span aria-hidden="true">↗</span>
          </a>,
        ]);
      }
      break;
    case "arxiv":
      facts.push(["Primary category", s("primary_category")], ["Version", s("version")], ["arXiv ID", item.external_id]);
      break;
  }
  facts.push(["Canonical URL", <span key="canon" className="mono">{item.canonical_url}</span>]);
  return facts.filter((f): f is [string, React.ReactNode] => f[1] !== undefined);
}
