'use client';

export default function GlobalError({ reset }: { reset: () => void }) {
  return (
    <div className="p-8 text-center md:text-left">
      <div className="mx-auto max-w-xl">
        <div className="page-eyebrow">Something went wrong</div>
        <h1 className="mt-2 text-2xl font-extrabold text-navy-950">
          This view failed to render
        </h1>
        <p className="mt-2 text-sm text-text-muted">
          The error has been contained to this screen — the rest of the app is
          still running. Retry, or head back to the dashboard.
        </p>
        <div className="mt-6 flex flex-wrap items-center gap-3">
          <button onClick={reset} className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white transition-colors hover:bg-navy-800">
            Try again
          </button>
          <a href="/" className="rounded-xl border border-border-subtle px-4 py-2 text-sm font-semibold text-navy-950 transition-colors hover:bg-slate-50">
            Back to dashboard
          </a>
        </div>
      </div>
    </div>
  );
}
