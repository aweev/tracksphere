'use client';

import { useState, type ReactNode } from 'react';
import type { RiskBreakdown } from '@/lib/api';

export type { RiskBreakdown };

/** Measured facts the server already computed (siblings of the breakdown on
 *  Shipment/Alert). Formatting only — no weights, no thresholds re-derived. */
export interface RiskFacts {
  dwellHours?: number;
  expectedDwellHours?: number;
  staleHours?: number;
  valueAtRisk?: number;
  customerNotified?: boolean;
  recalculatedAt?: string;
}

/** Where each line's evidence lives. Absent link = plain line, never a lie. */
export interface RiskLinks {
  timelineHref?: string;
  alertsHref?: string;
}

function span(hours: number): string {
  if (hours < 48) return `${Math.round(hours)}h`;
  const d = hours / 24;
  return `${d >= 20 ? Math.round(d) : d.toFixed(1)}d`;
}

function ago(iso?: string): string | null {
  if (!iso) return null;
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

/**
 * Why-this-risk in under 3 seconds. Renders exactly what the server computed —
 * points from the breakdown, measurements from the evidence fields and facts.
 * The one-line format names term, points, and proof together so the operator
 * never does mental arithmetic. Biggest lever turns the score into an action.
 */
export function RiskTooltip({
  children,
  score,
  breakdown,
  facts,
  links,
}: {
  children: ReactNode;
  score: number;
  breakdown: RiskBreakdown;
  facts?: RiskFacts;
  links?: RiskLinks;
}) {
  const [isOpen, setIsOpen] = useState(false);

  const dwellEv =
    facts?.dwellHours !== undefined && facts?.expectedDwellHours
      ? `${span(facts.dwellHours)} vs ${span(facts.expectedDwellHours)} norm`
      : (breakdown.dwellRatio ?? 0) > 1
        ? `${(breakdown.dwellRatio ?? 0).toFixed(1)}× lane norm`
        : null;
  const staleEv =
    facts?.staleHours !== undefined && breakdown.staleThresholdHours
      ? `${span(facts.staleHours)} silent vs ~${span(breakdown.staleThresholdHours)} expected`
      : facts?.staleHours !== undefined
        ? `${span(facts.staleHours)} silent`
        : null;
  const nCrit = breakdown.openCritical ?? (breakdown.critical > 0 ? 1 : 0);
  const nOther = breakdown.openOther ?? (breakdown.alerts > 0 ? 1 : 0);
  const alertEv =
    nCrit + nOther > 0
      ? `${nCrit} critical${nOther > 0 ? ` + ${nOther} other` : ''} open`
      : null;
  const slipEv =
    (breakdown.etaSlipHours ?? 0) > 0 ? `slipped ${span(breakdown.etaSlipHours ?? 0)}` : null;

  const line = (
    key: string,
    label: string,
    points: number,
    ev: string | null,
    href?: string,
  ) =>
    points > 0 && (
      <div key={key} className="flex items-baseline justify-between gap-2 text-xs">
        <span className="min-w-0 text-slate-600">
          {label}{' '}
          <span className="font-semibold text-navy-950">+{Math.round(points)}</span>
          {ev ? <span className="text-slate-500"> ({ev})</span> : null}{' '}
          {href ? (
            <a
              href={href}
              onClick={(e) => e.stopPropagation()}
              className="font-semibold text-accent-700 hover:underline"
            >
              evidence →
            </a>
          ) : null}
        </span>
      </div>
    );

  const items = [
    line('dwell', 'Dwell', breakdown.dwell, dwellEv, links?.timelineHref),
    line('slip', 'ETA slip', breakdown.slip, slipEv, links?.timelineHref),
    line('stale', 'Carrier silence', breakdown.stale, staleEv, links?.timelineHref),
    (breakdown.critical > 0 || breakdown.alerts > 0) &&
      line(
        'alerts',
        'Open alerts',
        breakdown.critical + breakdown.alerts,
        alertEv,
        links?.alertsHref,
      ),
    breakdown.valueKnown &&
      (breakdown.value > 0 ? (
        <div key="value" className="flex items-baseline justify-between gap-2 text-xs">
          <span className="min-w-0 text-slate-600">
            Declared value at risk{' '}
            <span className="font-semibold text-navy-950">+{Math.round(breakdown.value)}</span>
            {facts?.valueAtRisk ? (
              <span className="text-slate-500">
                {' '}
                (${Math.round(facts.valueAtRisk).toLocaleString()})
              </span>
            ) : null}
          </span>
        </div>
      ) : (
        <div key="value" className="flex justify-between text-xs">
          <span className="text-slate-600">Declared value</span>
          <span className="text-slate-500">not provided</span>
        </div>
      )),
    breakdown.relief !== 0 && (
      <div key="notified" className="flex justify-between text-xs">
        <span className="text-slate-600">Customer already notified</span>
        <span className="font-semibold text-emerald-600">{Math.round(breakdown.relief)}</span>
      </div>
    ),
  ].filter(Boolean);

  // Biggest lever: the largest ACTIONABLE term becomes next-step language.
  // Value is exposure, not a lever — it never wins. Relief is already banked.
  const levers: Array<{ points: number; text: string; href?: string }> = [];
  if (breakdown.critical + breakdown.alerts > 0)
    levers.push({
      points: breakdown.critical + breakdown.alerts,
      text: `Clear ${nCrit + nOther} open alert${nCrit + nOther === 1 ? '' : 's'} → up to −${Math.round(breakdown.critical + breakdown.alerts)}`,
      href: links?.alertsHref,
    });
  if (breakdown.stale > 0)
    levers.push({
      points: breakdown.stale,
      text: `Get a carrier ping — silence is +${Math.round(breakdown.stale)}`,
      href: links?.timelineHref,
    });
  if (breakdown.dwell > 0)
    levers.push({
      points: breakdown.dwell,
      text: `Move the box — every day over norm holds +${Math.round(breakdown.dwell)}`,
      href: links?.timelineHref,
    });
  if (breakdown.slip > 0)
    levers.push({
      points: breakdown.slip,
      text: `Confirm the revised ETA with the customer (+${Math.round(breakdown.slip)} of doubt)`,
      href: links?.timelineHref,
    });
  levers.sort((a, b) => b.points - a.points);
  const lever = items.length > 0 ? levers[0] : undefined;

  const recalc = ago(facts?.recalculatedAt);

  return (
    <div
      className="relative inline-block"
      tabIndex={0}
      role="button"
      aria-describedby={isOpen ? 'risk-breakdown-tip' : undefined}
      aria-label={`Risk score ${score} of 100. Activate for breakdown.`}
      onFocus={() => setIsOpen(true)}
      onBlur={(e) => {
        // Keep open while focus moves WITHIN (evidence links are tabbable).
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setIsOpen(false);
      }}
      onMouseEnter={() => setIsOpen(true)}
      onMouseLeave={() => setIsOpen(false)}
      onClick={() => setIsOpen((v) => !v)}
      onKeyDown={(e) => {
        if (e.key === 'Escape') setIsOpen(false);
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          setIsOpen((v) => !v);
        }
      }}
    >
      {children}
      {isOpen && (
        <div
          id="risk-breakdown-tip"
          role="tooltip"
          className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 z-50 min-w-[280px] max-w-[320px] rounded-xl bg-white p-4 shadow-lg ring-1 ring-slate-200 animate-in fade-in-0 zoom-in-95 max-w-[calc(100vw-2rem)]"
        >
          <div className="flex items-center justify-between mb-3">
            <h4 className="text-sm font-bold text-navy-950">Risk Score: {score}/100</h4>
            <button
              onClick={(e) => {
                e.stopPropagation();
                setIsOpen(false);
              }}
              className="text-slate-500 hover:text-slate-700"
              aria-label="Close tooltip"
            >
              ✕
            </button>
          </div>
          <div className="space-y-2 text-xs">
            {items}
            {items.length === 0 && (
              <div className="text-center text-slate-500 py-2">
                No risk factors — shipment is on track
              </div>
            )}
          </div>
          {lever ? (
            <p className="mt-3 rounded-lg bg-slate-50 px-2.5 py-2 text-xs text-slate-600 ring-1 ring-slate-200">
              <span className="font-bold text-navy-950">Biggest lever:</span> {lever.text}{' '}
              {lever.href ? (
                <a
                  href={lever.href}
                  onClick={(e) => e.stopPropagation()}
                  className="font-semibold text-accent-700 hover:underline"
                >
                  act →
                </a>
              ) : null}
            </p>
          ) : null}
          <div className="mt-3 pt-3 border-t border-slate-100 text-[10px] text-slate-500">
            {recalc ? (
              <>
                Recalculated {recalc} · refreshes every ~15 min.{' '}
              </>
            ) : null}
            Computed server-side from dwell vs lane norm, carrier silence, open
            alerts, declared value, and notification state.
          </div>
        </div>
      )}
    </div>
  );
}
