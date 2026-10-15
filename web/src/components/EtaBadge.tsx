'use client';

import type { Shipment } from '@/lib/api';

export type EtaSource = NonNullable<Shipment['etaSource']> | string | undefined;

/**
 * ETA provenance badge. NEVER render a heuristic as carrier truth:
 * `carrier` renders bare, `estimated`/`lane_model` get an `est` badge with
 * distinct labels, `none`/missing renders an honest "No ETA yet" state
 * (ambiguity aversion — never "—").
 */
export function EtaBadge({ source, confidence }: { source: EtaSource; confidence?: number }) {
  if (source === 'carrier') return null;
  if (source === 'lane_model') {
    return (
      <span
        title={`Lane-model estimate${typeof confidence === 'number' ? ` (${confidence}% confidence)` : ''} — not carrier-published`}
        aria-label="Lane-model estimated ETA"
        className="ml-1 inline-block rounded bg-amber-100 px-1 py-px align-middle text-[10px] font-bold uppercase tracking-wide text-amber-800"
      >
        est · model
      </span>
    );
  }
  if (source === 'estimated' || source === 'none' || source === undefined) {
    if (source === 'none') return null;
    return (
      <span
        title="Estimated ETA — not carrier-published"
        aria-label="Estimated ETA"
        className="ml-1 inline-block rounded bg-amber-100 px-1 py-px align-middle text-[10px] font-bold uppercase tracking-wide text-amber-800"
      >
        est
      </span>
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
