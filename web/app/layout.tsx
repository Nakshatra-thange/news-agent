import type { Metadata, Viewport } from "next";
import Link from "next/link";
import { ThemeToggle, themeInitScript } from "@/components/ThemeToggle";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Synergy", template: "%s · Synergy" },
  description: "A personal feed of AI developments from Hacker News, arXiv and GitHub.",
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#fbfbfa" },
    { media: "(prefers-color-scheme: dark)", color: "#121314" },
  ],
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // The theme script may set data-theme before hydration.
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
      </head>
      <body>
        <a className="skip" href="#main">
          Skip to content
        </a>
        <header className="site-header">
          <div className="container header-inner">
            <Link href="/" className="brand">
              <span className="brand-mark" aria-hidden="true" />
              Synergy
            </Link>
            <ThemeToggle />
          </div>
        </header>
        <main id="main" className="container">
          {children}
        </main>
      </body>
    </html>
  );
}
