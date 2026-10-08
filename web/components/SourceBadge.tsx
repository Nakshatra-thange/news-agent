import { sourceTypeLabel } from "@/lib/format";

/** A small colored mark identifying the source type. */
export function SourceBadge({ type }: { type: string }) {
  return (
    <span className="badge" data-source={type}>
      {sourceTypeLabel(type)}
    </span>
  );
}
