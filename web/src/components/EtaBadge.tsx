'use client';

import type { Shipment } from '@/lib/api';

export type EtaSource = NonNullable<Shipment['etaSource']> | string | undefined;

/**
 * ETA provenance badge. NEVER render a heuristic as carrier truth:
 * `carrier` renders bare, `estimated`/`lane_model` get an `est` badge with
 * distinct labels, `none`/missing renders an honest "No ETA yet" state
 * (ambiguity aversion — never "—").
 *
 * Lane evidence: when the server learned this lane (≥5 samples), a muted
 * `norm 18d · n=42` chip names the norm behind a lane-model ETA on tap
 * (title + aria). Below the minimum the chip never renders — "collecting
 * lane data" is said in words where space allows, never as a fake number.
 */
export function EtaBadge({
  source,
  confidence,
  laneNormDays,
  laneSamples,
  origin,
  destination,
}: {
  source: EtaSource;
  confidence?: number;
  laneNormDays?: number;
  laneSamples?: number;
  origin?: string;
  destination?: string;
}) {
  if (source === 'carrier') return null;
  const norm =
    laneNormDays != null && (laneSamples ?? 0) >= 5 ? (
      <span
        title={`Learned transit norm${origin && destination ? ` ${origin}→${destination}` : ''}: ${laneNormDays.toFixed(laneNormDays < 20 ? 1 : 0)} days over ${laneSamples} delivered shipments`}
        aria-label={`Lane norm ${laneNormDays.toFixed(1)} days from ${laneSamples} shipments`}
        className="ml-1 inline-block rounded bg-slate-100 px-1 py-px align-middle text-[10px] font-semibold text-slate-600"
      >
        norm {laneNormDays.toFixed(laneNormDays < 20 ? 1 : 0)}d · n={laneSamples}
      </span>
    ) : null;
  if (source === 'lane_model') {
    return (
      <>
        <span
          title={`Lane-model estimate${typeof confidence === 'number' ? ` (${confidence}% confidence)` : ''} — not carrier-published`}
          aria-label="Lane-model estimated ETA"
          className="ml-1 inline-block rounded bg-amber-100 px-1 py-px align-middle text-[10px] font-bold uppercase tracking-wide text-amber-800"
        >
          est · model
        </span>
        {norm}
      </>
    );
  }
  if (source === 'estimated' || source === 'none' || source === undefined) {
    if (source === 'none') return null;
    return (
      <>
        <span
          title="Estimated ETA — not carrier-published"
          aria-label="Estimated ETA"
          className="ml-1 inline-block rounded bg-amber-100 px-1 py-px align-middle text-[10px] font-bold uppercase tracking-wide text-amber-800"
        >
          est
        </span>
        {norm}
      </>
    );
  }
  return null;
}

/** Human ETA line with provenance built in. */
export function EtaLine({
  eta,
  source,
  confidence,
  lastPing,
  location,
}: {
  eta?: string;
  source: EtaSource;
  confidence?: number;
  lastPing?: string;
  location?: string;
}) {
  if (!eta) {
    const ping = lastPing ? ` — last ping ${lastPing}${location ? ` at ${location}` : ''}` : '';
    return <span className="text-xs text-slate-600">No ETA yet{ping}</span>;
  }
  return (
    <span>
      {new Date(eta).toLocaleString()}
      <EtaBadge source={source} confidence={confidence} />
    </span>
  );
}
