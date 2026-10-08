"use client";

/** Flips between light and dark, overriding the system preference, and
 *  remembers the choice. Both icons are always rendered and CSS shows the
 *  right one, so server and client markup never differ. */
export function ThemeToggle() {
  function toggle() {
    const root = document.documentElement;
    const dark =
      root.dataset.theme === "dark" ||
      (root.dataset.theme !== "light" && window.matchMedia("(prefers-color-scheme: dark)").matches);
    const next = dark ? "light" : "dark";
    root.dataset.theme = next;
    try {
      localStorage.setItem("synergy-theme", next);
    } catch {
      // Storage unavailable (private mode): the choice lasts for this page.
    }
  }

  return (
    <button type="button" className="icon-button theme-toggle" onClick={toggle} aria-label="Toggle dark mode">
      <svg className="icon-moon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
        <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" fill="currentColor" />
      </svg>
      <svg className="icon-sun" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
        <circle cx="12" cy="12" r="4.5" fill="currentColor" />
        <g stroke="currentColor" strokeWidth="2" strokeLinecap="round">
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </g>
      </svg>
    </button>
  );
}

/** Runs before first paint (inlined in <head>) so a stored choice applies
 *  without a flash of the wrong theme. */
export const themeInitScript = `try{var t=localStorage.getItem("synergy-theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t}catch(e){}`;
