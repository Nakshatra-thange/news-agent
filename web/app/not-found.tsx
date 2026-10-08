import { StatePanel } from "@/components/StatePanel";

export default function NotFound() {
  return (
    <StatePanel title="Page not found" action={{ href: "/", label: "Back to the feed" }}>
      <p>There’s nothing at this address.</p>
    </StatePanel>
  );
}
