import type { Metadata, Viewport } from "next";
import { Inter, Playfair_Display, Source_Serif_4 } from "next/font/google";
import Link from "next/link";
import { ThemeToggle, themeInitScript } from "@/components/ThemeToggle";
import "./globals.css";

// Editorial serif for headlines, a book serif for summaries and a plain
// sans for controls and metadata. Self-hosted by next/font at build time.
const display = Playfair_Display({ subsets: ["latin"], weight: ["600", "700", "800"], variable: "--font-display" });
const serif = Source_Serif_4({ subsets: ["latin"], style: ["normal", "italic"], variable: "--font-serif" });
const sans = Inter({ subsets: ["latin"], variable: "--font-sans" });

export const metadata: Metadata = {
  title: { default: "Synergy", template: "%s · Synergy" },
  description: "A personal feed of AI developments from Hacker News, arXiv and GitHub.",
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#efe6d2" },
    { media: "(prefers-color-scheme: dark)", color: "#12161f" },
  ],
};

const editionDate = new Intl.DateTimeFormat("en", { weekday: "long", month: "long", day: "numeric", year: "numeric" });

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // The theme script may set data-theme before hydration.
    <html lang="en" suppressHydrationWarning className={`${display.variable} ${serif.variable} ${sans.variable}`}>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
      </head>
      <body>
        <a className="skip" href="#main">
          Skip to content
        </a>
        <header className="site-header">
          <div className="container dateline">
            <span className="dateline-date">{editionDate.format(new Date())}</span>
            <div className="header-actions">
              <Link href="/preferences" className="header-link">
                Preferences
              </Link>
              <ThemeToggle />
            </div>
          </div>
          <div className="container masthead">
            <Link href="/" className="brand" aria-label="Synergy, front page">
              {/* Plain <img>: a fixed-size local asset needs no optimizer. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                className="brand-emblem"
                src="/brand/emblem-240.jpg"
                srcSet="/brand/emblem-240.jpg 1x, /brand/emblem-480.jpg 2x"
                width={72}
                height={72}
                alt=""
              />
              <span className="brand-name">Synergy</span>
            </Link>
            <p className="brand-tagline">Dispatches from the frontier of AI · Hacker News · arXiv · GitHub</p>
          </div>
        </header>
        <main id="main" className="container">
          {children}
        </main>
        <footer className="site-footer container">
          <p>Summaries are written by a language model from each source’s own text. Always check the original.</p>
        </footer>
      </body>
    </html>
  );
}
