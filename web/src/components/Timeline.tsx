import type { ShipmentEvent } from '@/lib/api';
import { relativeTime } from '@/lib/format';

interface CodeCopy {
  ops: string;
  customer: string;
  glyph: string;
  critical?: boolean;
  done?: boolean;
}

/** Machine code → human voices. Ops gets precision, customers get calm. */
const COPY: Record<string, CodeCopy> = {
  BOOKED: { ops: 'Booking confirmed', customer: 'Order received', glyph: '●' },
  DEPARTED: { ops: 'Departed origin', customer: 'On its way', glyph: '▲' },
  ARRIVED: { ops: 'Arrived at facility', customer: 'Arrived nearby', glyph: '◐' },
  CUSTOMS_HOLD: {
    ops: 'Customs hold — clearance docs may be required',
    customer: 'Clearance in progress — arriving a little later',
    glyph: '◆', critical: true,
  },
  PORT_CONGESTION: {
    ops: 'Port congestion — arrival may slip',
    customer: 'Busy port — slight delay possible',
    glyph: '◆',
  },
  ETA_REVISED: { ops: 'ETA revised by carrier', customer: 'New arrival estimate', glyph: '◷' },
  OUT_FOR_DELIVERY: { ops: 'Out for delivery', customer: 'Arriving today', glyph: '▲' },
  DELIVERED: { ops: 'Delivered to consignee', customer: 'Delivered', glyph: '✓', done: true },
  EXCEPTION: {
    ops: 'Exception flagged — needs operator review',
    customer: 'Hiccup on the way — we are on it',
    glyph: '◆', critical: true,
  },
};

function copyFor(code: string): CodeCopy {
  const hit = COPY[code];
  if (hit) return hit;
  const words = code.toLowerCase().replace(/_/g, ' ');
  const titled = words.replace(/\b\w/g, (c) => c.toUpperCase());
  return { ops: titled, customer: titled, glyph: '●' };
}

export function Timeline({
  events,
  audience = 'ops',
}: {
  events: ShipmentEvent[];
  audience?: 'ops' | 'customer';
}) {
  return (
    <ol className="relative ml-3 space-y-6 border-l-2 border-slate-200 pl-6" aria-label="Shipment timeline">
      {[...events].reverse().map((e) => {
        const c = copyFor(e.code);
        return (
          <li key={e.id} className="relative">
            <span
              aria-hidden="true"
              className={`absolute -left-[31px] top-1 flex h-3 w-3 items-center justify-center rounded-full text-[8px] ring-4 ring-white ${
                c.critical
                  ? 'bg-red-600 text-white'
                  : c.done
                    ? 'bg-emerald-600 text-white'
                    : 'bg-accent-500 text-white'
              }`}
            />
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="text-sm font-bold text-navy-950">
                {audience === 'customer' ? c.customer : c.ops}
              </span>
              <time
                className="font-mono text-xs text-slate-600"
                dateTime={new Date(e.occurredAt).toISOString()}
                title={new Date(e.occurredAt).toLocaleString()}
              >
                {relativeTime(e.occurredAt)}
              </time>
            </div>
            {audience === 'ops' && e.description ? (
              <div className="text-sm text-slate-600">{e.description}</div>
            ) : null}
            {e.location ? (
              <div className="text-xs font-semibold uppercase tracking-wide text-slate-600">
                {e.location}
              </div>
            ) : null}
            {audience === 'ops' ? (
              <div className="mt-0.5 font-mono text-[11px] text-slate-500">
                {e.code} · {new Date(e.occurredAt).toLocaleString()}
                {e.source && e.source !== 'webhook' ? ` · ${e.source}` : ''}
              </div>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}

export function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-slate-600">{label}</dt>
      <dd className="text-right font-semibold capitalize text-navy-950">{value}</dd>
    </div>
  );
}