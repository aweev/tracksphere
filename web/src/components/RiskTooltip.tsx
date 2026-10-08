'use client';

import { useState, type ReactNode } from 'react';
import type { RiskBreakdown } from '@/lib/api';

export type { RiskBreakdown };

/**
 * Tooltip that explains the risk score breakdown.
 * Renders exactly what the server computed — the breakdown prop is the
 * persisted Score() output, not a client-side re-derivation.
 */
export function RiskTooltip({
  children,
  score,
  breakdown,
}: {
  children: ReactNode;
  score: number;
  breakdown: RiskBreakdown;
}) {
  const [isOpen, setIsOpen] = useState(false);

  const items = [
    breakdown.dwell > 0 && (
      <div key="dwell" className="flex justify-between text-xs">
        <span className="text-slate-600">Dwell over lane norm</span>
        <span className="font-semibold text-orange-600">+{Math.round(breakdown.dwell)}</span>
      </div>
    ),
    breakdown.slip > 0 && (
      <div key="slip" className="flex justify-between text-xs">
        <span className="text-slate-600">ETA slipped</span>
        <span className="font-semibold text-orange-600">+{Math.round(breakdown.slip)}</span>
      </div>
    ),
    breakdown.stale > 0 && (
      <div key="stale" className="flex justify-between text-xs">
        <span className="text-slate-600">Carrier silence</span>
        <span className="font-semibold text-red-600">+{Math.round(breakdown.stale)}</span>
      </div>
    ),
    (breakdown.critical > 0 || breakdown.alerts > 0) && (
      <div key="alerts" className="flex justify-between text-xs">
        <span className="text-slate-600">Open alerts</span>
        <span className="font-semibold text-amber-600">
          +{Math.round(breakdown.critical + breakdown.alerts)}
        </span>
      </div>
    ),
    breakdown.valueKnown &&
      (breakdown.value > 0 ? (
        <div key="value" className="flex justify-between text-xs">
          <span className="text-slate-600">Declared value at risk</span>
          <span className="font-semibold text-slate-600">+{Math.round(breakdown.value)}</span>
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

  return (
    <div
      className="relative inline-block"
      tabIndex={0}
      onFocus={() => setIsOpen(true)}
      onBlur={() => setIsOpen(false)}
      onMouseEnter={() => setIsOpen(true)}
      onMouseLeave={() => setIsOpen(false)}
    >
      {children}
      {isOpen && (
        <div className="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 z-50 min-w-[280px] max-w-[320px] rounded-xl bg-white p-4 shadow-lg ring-1 ring-slate-200 animate-in fade-in-0 zoom-in-95">
          <div className="flex items-center justify-between mb-3">
            <h4 className="text-sm font-bold text-navy-950">Risk Score: {score}/100</h4>
            <button
              onClick={(e) => { e.stopPropagation(); setIsOpen(false); }}
              className="text-slate-400 hover:text-slate-600"
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
          <div className="mt-3 pt-3 border-t border-slate-100 text-[10px] text-slate-500">
            Scores are computed from: dwell vs lane norm, carrier silence, open alerts, declared value, and customer notification status.
          </div>
        </div>
      )}
    </div>
  );
}
