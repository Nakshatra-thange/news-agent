import type { Metadata, Viewport } from "next";
import { DM_Sans, Fraunces } from "next/font/google";
import Link from "next/link";
import { Intro, introInitScript } from "@/components/Intro";
import { EggChair } from "@/components/RetroObjects";
import { ThemeToggle, themeInitScript } from "@/components/ThemeToggle";
import { feedHref } from "@/lib/api";
import "./globals.css";

// A soft, chunky serif (italic for display) and a friendly sans for
// everything else. Self-hosted by next/font at build time.
const display = Fraunces({
  subsets: ["latin"],
  style: ["normal", "italic"],
  axes: ["SOFT", "WONK", "opsz"],
  variable: "--font-display",
});
const sans = DM_Sans({ subsets: ["latin"], variable: "--font-sans" });

export const metadata: Metadata = {
  title: { default: "Synergy", template: "%s · Synergy" },
  description: "Bite-sized AI news from Hacker News, arXiv and GitHub, as flashcards.",
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#bcdcf0" },
    { media: "(prefers-color-scheme: dark)", color: "#1b2430" },
  ],
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // The theme and intro scripts may set data-theme and data-intro before hydration.
    <html lang="en" suppressHydrationWarning className={`${display.variable} ${sans.variable}`}>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
        <script dangerouslySetInnerHTML={{ __html: introInitScript }} />
      </head>
      <body>
        <Intro />
        <a className="skip" href="#main">
          Skip to content
        </a>
        <div className="sheet">
          <header className="topbar">
            <Link href="/" className="wordmark" aria-label="Synergy, front page">
              Synergy
            </Link>
            <nav className="pill-nav" aria-label="Main">
              <Link href="/">Latest</Link>
              <Link href={feedHref({ view: "foryou" })}>For you</Link>
              <Link href="/preferences">Preferences</Link>
            </nav>
            <div className="topbar-actions">
              <Link href="/#feed" className="icon-button" aria-label="Search the feed">
                <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
                  <circle cx="11" cy="11" r="6.5" fill="none" stroke="currentColor" strokeWidth="2.4" />
                  <path d="M16 16l4.5 4.5" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" />
                </svg>
              </Link>
              <ThemeToggle />
            </div>
          </header>
          <main id="main">{children}</main>
          <footer className="site-footer">
            <div className="footer-copy">
              <h2 className="script-heading">Flip, skim, go deeper</h2>
              <p>
                Every card is one story from Hacker News, arXiv or GitHub. Flip it for a short summary written by a
                language model from the source’s own text, then always check the original.
              </p>
            </div>
            <div className="footer-art">
              <EggChair id="footer-chair" className="float" />
            </div>
          </footer>
        </div>
      </body>
    </html>
  );
}
