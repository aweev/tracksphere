'use client';

import { useEffect, useState } from 'react';

export type ConnState = 'live' | 'connecting' | 'reconnecting' | 'offline';

/**
 * Connection + freshness indicator. Trust calibration in one line: a green
 * dot that says "Live · updated 12s ago" is a promise the stream keeps; an
 * amber "Reconnecting — last confirmed snapshot" tells the truth when it
 * doesn't. Never show a green dot on a dead socket.
 */
export function ConnectionDot({
  state,
  updatedAt,
}: {
  state: ConnState;
  updatedAt?: string | null;
}) {
  const ago = useAgo(updatedAt);
  const conf =
    state === 'live'
      ? { dot: 'bg-emerald-500', pulse: true, label: 'Live' }
      : state === 'reconnecting'
        ? { dot: 'bg-amber-500', pulse: false, label: 'Reconnecting' }
        : state === 'offline'
          ? { dot: 'bg-slate-400', pulse: false, label: 'Offline' }
          : { dot: 'bg-slate-400', pulse: true, label: 'Connecting' };
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-slate-600" role="status" aria-live="polite">
      <span className="relative flex h-2 w-2" aria-hidden="true">
        {conf.pulse ? (
          <span className={`absolute inline-flex h-full w-full animate-ping rounded-full opacity-60 ${conf.dot}`} />
        ) : null}
        <span className={`relative inline-flex h-2 w-2 rounded-full ${conf.dot}`} />
      </span>
      <span>
        {conf.label}
        {ago ? ` · updated ${ago}` : ''}
        {state === 'reconnecting' ? ' — last confirmed snapshot' : ''}
      </span>
    </span>
  );
}

/** Ticking "Xs ago" label. Re-renders every 15s (Zeigarnik open loop made
 *  visible); static text when there is nothing to count from. */
export function useAgo(iso?: string | null): string | null {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!iso) return;
    const t = setInterval(() => setTick((n) => n + 1), 15000);
    return () => clearInterval(t);
  }, [iso]);
  if (!iso) return null;
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 5) return 'just now';
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}
