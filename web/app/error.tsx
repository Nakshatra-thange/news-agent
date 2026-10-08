"use client";

// Last-resort boundary for unexpected rendering errors. Expected API
// failures are handled where they happen and never reach it.
export default function ErrorPage({ reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <section className="state" data-tone="error" role="alert">
      <h2>Something went wrong</h2>
      <div className="state-body">
        <p>This page failed to render. Trying again usually helps.</p>
      </div>
      <button type="button" className="button" onClick={reset}>
        Try again
      </button>
    </section>
  );
}
