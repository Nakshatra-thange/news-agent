import Link from "next/link";
import type { ReactNode } from "react";
import type { ApiError } from "@/lib/api";

/** A calm, centered message for empty, error and not-found states. */
export function StatePanel({
  title,
  children,
  action,
  tone = "neutral",
}: {
  title: string;
  children?: ReactNode;
  action?: { href: string; label: string };
  tone?: "neutral" | "error";
}) {
  return (
    <section className="state" data-tone={tone} role={tone === "error" ? "alert" : undefined}>
      <h2>{title}</h2>
      {children && <div className="state-body">{children}</div>}
      {action && (
        <Link className="button" href={action.href}>
          {action.label}
        </Link>
      )}
    </section>
  );
}

/** Explains an API failure without dumping internals. */
export function ApiErrorPanel({ error, retryHref }: { error: ApiError; retryHref: string }) {
  switch (error.kind) {
    case "network":
      return (
        <StatePanel tone="error" title="Can’t reach Synergy" action={{ href: retryHref, label: "Try again" }}>
          <p>The Synergy API isn’t responding. Make sure the backend is running (<code>make run</code>).</p>
        </StatePanel>
      );
    case "unavailable":
      return (
        <StatePanel tone="error" title="Synergy is temporarily unavailable" action={{ href: retryHref, label: "Try again" }}>
          <p>The API is up but can’t reach its database. This usually resolves on its own.</p>
        </StatePanel>
      );
    case "invalid":
      return (
        <StatePanel tone="error" title="Some filters aren’t valid" action={{ href: "/", label: "Clear filters" }}>
          {error.details && error.details.length > 0 ? (
            <ul>
              {error.details.map((d) => (
                <li key={d.field}>
                  <code>{d.field}</code> {d.message}
                </li>
              ))}
            </ul>
          ) : (
            <p>{error.message}</p>
          )}
        </StatePanel>
      );
    default:
      return (
        <StatePanel tone="error" title="Something went wrong" action={{ href: retryHref, label: "Try again" }}>
          <p>The API returned an unexpected error. Details are in the server log.</p>
        </StatePanel>
      );
  }
}
