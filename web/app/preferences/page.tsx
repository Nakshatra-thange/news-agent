import type { Metadata } from "next";
import { PreferencesForm } from "@/components/PreferencesForm";
import { ApiErrorPanel } from "@/components/StatePanel";
import { getActiveSources, getPreferences } from "@/lib/api";

export const metadata: Metadata = { title: "Preferences" };

/** Edits the topics and sources that the personalized ranked feed
 *  (sort=ranked&personalized=true) boosts. */
export default async function PreferencesPage() {
  const [prefs, sources] = await Promise.all([getPreferences(), getActiveSources()]);
  if (!prefs.ok) return <ApiErrorPanel error={prefs.error} retryHref="/preferences" />;
  if (!sources.ok) return <ApiErrorPanel error={sources.error} retryHref="/preferences" />;
  return <PreferencesForm initial={prefs.data} sources={sources.data} />;
}
