"use client";

import { useState } from "react";
import { toApiError, type Preferences, type Source } from "@/lib/api";

type Props = {
  initial: Preferences;
  /** Active sources to choose from. Preferred sources that are no longer
   *  active are listed too, so saving does not silently drop them. */
  sources: Source[];
};

/** Topics as comma-separated text plus source checkboxes. Saving replaces
 *  all preferences through PUT /api/v1/preferences. */
export function PreferencesForm({ initial, sources }: Props) {
  const [topics, setTopics] = useState(initial.topics.join(", "));
  const [selected, setSelected] = useState(() => new Set(initial.sources.map((s) => s.id)));
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<{ ok: boolean; text: string } | null>(null);
  const choices = [
    ...sources.map((s) => ({ id: s.id, name: s.name })),
    ...initial.sources.filter((p) => !sources.some((s) => s.id === p.id)),
  ];

  function toggle(id: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setStatus(null);
    try {
      const res = await fetch("/api/v1/preferences", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          topics: topics.split(",").map((t) => t.trim()).filter(Boolean),
          sources: [...selected],
        }),
      });
      if (!res.ok) {
        const err = await toApiError(res);
        const detail = err.details?.map((d) => d.message).join("; ");
        setStatus({ ok: false, text: detail ? `${err.message}: ${detail}` : err.message });
        return;
      }
      const saved = (await res.json()) as Preferences;
      setTopics(saved.topics.join(", "));
      setSelected(new Set(saved.sources.map((s) => s.id)));
      setStatus({ ok: true, text: "Saved." });
    } catch {
      setStatus({ ok: false, text: "Network error: check your connection and the Synergy API." });
    } finally {
      setSaving(false);
    }
  }

  return (
    <form className="prefs" onSubmit={save}>
      <h1>Preferences</h1>
      <p className="muted">
        In the personalized ranked feed, items matching a topic (a tag such as <span className="mono">cs.LG</span>,
        or an enrichment topic) or from a chosen source move up by 12 hours each.
      </p>
      <fieldset>
        <legend>Topics</legend>
        <input
          className="search"
          name="topics"
          value={topics}
          onChange={(e) => setTopics(e.target.value)}
          placeholder="agents, cs.LG, llm"
          aria-describedby="topics-hint"
        />
        <span id="topics-hint" className="muted">
          Comma-separated, not case-sensitive.
        </span>
      </fieldset>
      <fieldset>
        <legend>Sources</legend>
        {choices.length === 0 && <p className="muted">No active sources.</p>}
        {choices.map((s) => (
          <label key={s.id} className="check">
            <input type="checkbox" checked={selected.has(s.id)} onChange={() => toggle(s.id)} />
            {s.name}
          </label>
        ))}
      </fieldset>
      <div className="prefs-actions">
        <button type="submit" className="button primary" disabled={saving} aria-busy={saving}>
          {saving ? "Saving…" : "Save"}
        </button>
        <span className={status && !status.ok ? "inline-error" : "muted"} role="status">
          {status?.text}
        </span>
      </div>
    </form>
  );
}
