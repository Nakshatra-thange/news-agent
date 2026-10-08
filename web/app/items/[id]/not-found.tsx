import { StatePanel } from "@/components/StatePanel";

export default function ItemNotFound() {
  return (
    <StatePanel title="Item not found" action={{ href: "/", label: "Back to the feed" }}>
      <p>This link doesn’t point to an item Synergy knows. It may be mistyped or incomplete.</p>
    </StatePanel>
  );
}
