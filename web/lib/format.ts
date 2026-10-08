import type { Item, SourceType } from "./api";

export const sourceTypeLabels: Record<SourceType, string> = {
  hackernews: "Hacker News",
  arxiv: "arXiv",
  github: "GitHub",
};

export const kindLabels: Record<string, string> = {
  paper: "Paper",
  repository: "Repository",
  discussion: "Discussion",
  release: "Release",
};

export function sourceTypeLabel(t: string): string {
  return sourceTypeLabels[t as SourceType] ?? t;
}

export function kindLabel(k: string): string {
  return kindLabels[k] ?? k;
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
const steps: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
];

/** "3 hours ago", "yesterday", "just now". */
export function relativeTime(iso: string, now: Date = new Date()): string {
  const seconds = (new Date(iso).getTime() - now.getTime()) / 1000;
  for (const [unit, size] of steps) {
    if (Math.abs(seconds) >= size) {
      return rtf.format(Math.round(seconds / size), unit);
    }
  }
  return "just now";
}

const absoluteFormat = new Intl.DateTimeFormat("en", {
  dateStyle: "medium",
  timeStyle: "short",
  timeZone: "UTC",
});

/** "Oct 7, 2026, 5:59 PM UTC" */
export function absoluteTime(iso: string): string {
  return `${absoluteFormat.format(new Date(iso))} UTC`;
}

/** "github.com", without "www.". */
export function hostname(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

const compact = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

export function compactNumber(n: number): string {
  return compact.format(n);
}

function num(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function str(v: unknown): string | undefined {
  return typeof v === "string" && v !== "" ? v : undefined;
}

/** A few source-specific numbers worth showing at a glance. */
export function itemStats(item: Item): string[] {
  const m = item.metadata;
  const out: string[] = [];
  switch (item.source.type) {
    case "hackernews": {
      const points = num(m.points);
      const comments = num(m.num_comments);
      if (points !== undefined) out.push(`${compactNumber(points)} points`);
      if (comments !== undefined) out.push(`${compactNumber(comments)} comments`);
      break;
    }
    case "github": {
      const stars = num(m.stars);
      if (stars !== undefined) out.push(`★ ${compactNumber(stars)}`);
      const lang = str(m.language);
      if (lang) out.push(lang);
      break;
    }
    case "arxiv": {
      const cat = str(m.primary_category);
      if (cat) out.push(cat);
      break;
    }
  }
  return out;
}

/** "A. Author, B. Author and 3 others" */
export function authorLine(authors: string[], max = 3): string {
  if (authors.length <= max) return authors.join(", ");
  const rest = authors.length - max;
  return `${authors.slice(0, max).join(", ")} and ${rest} other${rest === 1 ? "" : "s"}`;
}
