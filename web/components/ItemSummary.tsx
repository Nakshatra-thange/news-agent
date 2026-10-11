import type { Item } from "@/lib/api";
import { absoluteTime, hostname, relativeTime } from "@/lib/format";

/** The item's stored summary, or an explicit fallback when there is none.
 *  The fallback never passes the source's own text off as a summary: it is
 *  labelled as coming from the source, and the original link stays one
 *  click away. No hooks, so it renders on the server and the client.
 *
 *  `variant="card"` clamps the text for the feed; `"detail"` shows it in
 *  full with its provenance. */
export function ItemSummary({ item, variant }: { item: Item; variant: "card" | "detail" }) {
  const s = item.summary;
  if (s) {
    return (
      <div className="summary" data-variant={variant}>
        {variant === "detail" && <h2 className="summary-label">The brief</h2>}
        <p className="summary-text">{s.text}</p>
        {variant === "detail" && (
          <p className="summary-note">
            Machine-written summary of the source’s own text ({s.model},{" "}
            <time dateTime={s.generated_at} title={absoluteTime(s.generated_at)}>
              {relativeTime(s.generated_at)}
            </time>
            ). It may omit or misread details; check the original.
          </p>
        )}
      </div>
    );
  }

  if (variant === "detail") {
    return (
      <div className="summary missing" data-variant="detail">
        <h2 className="summary-label">The brief</h2>
        <p className="summary-fallback">
          {item.description
            ? "No summary has been written for this story yet. The source’s own description follows below."
            : "The source gives only a headline, so there is nothing to summarize. Read it at the original source."}
        </p>
      </div>
    );
  }

  return (
    <div className="summary missing" data-variant="card">
      {item.description ? (
        <p className="summary-text source-text">
          <span className="from-source">From the source: </span>
          {item.description}
        </p>
      ) : (
        <p className="summary-fallback">
          The source gives only a headline. Read the full story on {hostname(item.url)}.
        </p>
      )}
    </div>
  );
}
