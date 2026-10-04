'use client';

import { Suspense, useEffect, useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty, RiskBadge, StatusPill } from '@/components/ui';
import { RiskTooltip, type RiskBreakdown } from '@/components/RiskTooltip';
import { api, type Shipment, type ShipmentStatus } from '@/lib/api';
import { CreateShipmentForm } from '@/components/CreateShipmentForm';

const STATUSES: Array<ShipmentStatus | ''> = [
  '', 'booked', 'in_transit', 'at_customs', 'out_for_delivery',
  'delivered', 'exception', 'cancelled',
];
const MODES = ['', 'ocean', 'air', 'road', 'rail'];
const PAGE_SIZE = 25;
const VIEWS_KEY = 'ts-saved-views';

interface SavedView {
  name: string;
  status: string;
  carrier: string;
  mode: string;
  search: string;
}

function loadViews(): SavedView[] {
  try {
    return JSON.parse(localStorage.getItem(VIEWS_KEY) ?? '[]');
  } catch {
    return [];
  }
}

/**
 * Client-side mirror of the server's risk weights, used only to explain a score
 * in the tooltip. The server remains authoritative for the score itself.
 *
 * These weights duplicate internal/readmodel in a second language, which is a
 * known drift hazard: the Go constants are wDwellMax 35, wStaleMax 25,
 * wCriticalAlert 15, wAlert 3, wValueMax 15 with value saturating at 10,000.
 * Phase 1 removes this duplication by having Score() return the breakdown and
 * persisting it on shipment_current. Until then the arithmetic lives here once
 * rather than copy-pasted at every call site.
 */
function riskBreakdown(s: Shipment): RiskBreakdown {
  return {
    dwell:
      s.dwellHours && s.expectedDwellHours && s.dwellHours > s.expectedDwellHours
        ? Math.round((s.dwellHours / s.expectedDwellHours - 1) * 35)
        : 0,
    stale: s.staleHours && s.expectedDwellHours
      ? Math.round(Math.max(0, s.staleHours / (s.expectedDwellHours / 6) - 1) * 25)
      : 0,
    alerts:
      (s.openAlerts ?? 0) > 0
        ? (s.criticalAlerts ?? 0) * 15 + (s.openAlerts ?? 0) * 3
        : 0,
    valueAtRisk: s.valueAtRisk ? Math.min((s.valueAtRisk / 10000) * 15, 15) : 0,
    customerNotified: s.customerNotified ?? false,
    dwellRatio: s.dwellRatio,
    staleThreshold: s.expectedDwellHours ? s.expectedDwellHours / 6 : 24,
  };
}

export default function ShipmentsPage() {
  return (
    <RequireAuth>
      <Suspense fallback={<div className="p-8 text-slate-400" aria-busy="true">Loading…</div>}>
        <ShipmentsBody />
      </Suspense>
    </RequireAuth>
  );
}

function ShipmentsBody() {
  const qc = useQueryClient();
  const searchParams = useSearchParams();
  const [status, setStatus] = useState(searchParams.get('status') ?? '');
  const [carrier, setCarrier] = useState('');
  const [mode, setMode] = useState('');
  const [search, setSearch] = useState('');
  const [debounced, setDebounced] = useState('');
  const [sort, setSort] = useState<'risk' | 'eta' | 'recent'>('risk');
  const [serverSort, setServerSort] = useState<string>('risk');
  const [riskTier, setRiskTier] = useState('');
  const [page, setPage] = useState(0);
  const [showCreate, setShowCreate] = useState(false);
  const [views, setViews] = useState<SavedView[]>([]);

  useEffect(() => setViews(loadViews()), []);
  useEffect(() => {
    const t = setTimeout(() => {
      setDebounced(search.trim());
      setPage(0);
    }, 300);
    return () => clearTimeout(t);
  }, [search]);

  const query = useMemo(() => {
    const params = new URLSearchParams({
      limit: String(PAGE_SIZE),
      offset: String(page * PAGE_SIZE),
      sort: serverSort,
    });
    if (status) params.set('status', status);
    if (carrier.trim()) params.set('carrier', carrier.trim().toLowerCase());
    if (riskTier) params.set('riskTier', riskTier);
    if (debounced) params.set('search', debounced);
    return params.toString();
  }, [status, carrier, debounced, page, serverSort, riskTier]);

  const { data: result, isLoading, error, refetch } = useQuery({
    queryKey: ['shipments', { status, carrier, debounced, page, serverSort, riskTier }],
    queryFn: async () => api.get<Shipment[]>(`/api/v1/shipments?${query}`),
  });

  const shipments = useMemo(() => {
    const rows = [...(result?.data ?? [])];
    if (mode) rows.sort((a, b) => a.mode.localeCompare(b.mode));
    return rows;
  }, [result, mode]);

  const total = result?.meta?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const saveView = () => {
    const name = window.prompt('Name this view (e.g. Ocean exceptions)');
    if (!name?.trim()) return;
    const next = [...views, { name: name.trim(), status, carrier, mode, search }];
    setViews(next);
    localStorage.setItem(VIEWS_KEY, JSON.stringify(next));
  };

  return (
    <div className="p-4 md:p-8">
      <header className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-extrabold text-navy-950">Shipments</h1>
          <p className="mt-1 text-sm text-slate-600" aria-live="polite">
            {total !== undefined ? `${total} total · page ${page + 1} of ${pages}` : 'Loading…'}
          </p>
        </div>
        <button
          onClick={() => setShowCreate((v) => !v)}
          className="rounded-xl bg-accent-500 px-4 py-2.5 text-sm font-semibold text-white hover:bg-accent-600 focus-visible:ring-2 focus-visible:ring-accent-500"
        >
          {showCreate ? 'Close' : '+ New shipment'}
        </button>
      </header>

      {showCreate ? (
        <CreateShipmentForm
          onCreated={() => {
            setShowCreate(false);
            void qc.invalidateQueries({ queryKey: ['shipments'] });
          }}
        />
      ) : null}

      <div className="mb-4 flex flex-wrap items-center gap-3" role="search" aria-label="Shipment filters">
        <label htmlFor="shipment-search" className="sr-only">Search shipments</label>
        <input
          id="shipment-search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search tracking # or reference…  (⌘K)"
          className="w-72 rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        />
        <label htmlFor="status-filter" className="sr-only">Status filter</label>
        <select
          id="status-filter"
          value={status}
          onChange={(e) => { setStatus(e.target.value); setPage(0); }}
          className="rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        >
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s === '' ? 'All statuses' : s.replace('_', ' ')}
            </option>
          ))}
        </select>
        <label htmlFor="carrier-filter" className="sr-only">Carrier filter</label>
        <input
          id="carrier-filter"
          value={carrier}
          onChange={(e) => { setCarrier(e.target.value); setPage(0); }}
          placeholder="Carrier (e.g. maersk)"
          className="w-44 rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        />
        <label htmlFor="mode-filter" className="sr-only">Mode filter</label>
        <select
          id="mode-filter"
          value={mode}
          onChange={(e) => setMode(e.target.value)}
          className="rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        >
          {MODES.map((m) => (
            <option key={m} value={m}>{m === '' ? 'All modes' : m}</option>
          ))}
        </select>
        <label htmlFor="risk-filter" className="sr-only">Risk tier filter</label>
        <select
          id="risk-filter"
          value={riskTier}
          onChange={(e) => { setRiskTier(e.target.value); setPage(0); }}
          className="rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        >
          <option value="">Any risk</option>
          <option value="critical">Critical only</option>
          <option value="at_risk">At risk</option>
          <option value="watch">Watch</option>
          <option value="clear">On track</option>
        </select>
        <label htmlFor="sort-select" className="sr-only">Sort</label>
        <select
          id="sort-select"
          value={sort}
          onChange={(e) => {
            const v = e.target.value as typeof sort;
            setSort(v);
            setPage(0);
            setServerSort(v === 'risk' ? 'risk' : v);
          }}
          className="rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        >
          <option value="risk">Risk first</option>
          <option value="eta">Earliest ETA</option>
          <option value="recent">Newest first</option>
        </select>
        <button
          type="button"
          onClick={saveView}
          className="rounded-xl border border-slate-200 px-3 py-2 text-xs font-semibold text-navy-950 hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-accent-500"
        >
          Save view
        </button>
      </div>

      {views.length > 0 ? (
        <div className="mb-4 flex flex-wrap gap-2" aria-label="Saved views">
          {views.map((v) => (
            <button
              key={v.name}
              type="button"
              onClick={() => {
                setStatus(v.status); setCarrier(v.carrier); setMode(v.mode);
                setSearch(v.search); setPage(0);
              }}
              className="rounded-full bg-slate-100 px-3 py-1 text-xs font-semibold text-navy-950 hover:bg-slate-200 focus-visible:ring-2 focus-visible:ring-accent-500"
            >
              {v.name}
            </button>
          ))}
        </div>
      ) : null}

      <Card>
        {isLoading ? (
          <div className="animate-pulse space-y-3" aria-label="Loading shipments" role="status">
            {[0, 1, 2, 3].map((i) => (
              <div key={i} className="h-12 rounded-xl bg-slate-100" />
            ))}
          </div>
        ) : error ? (
          <div className="p-4 text-center">
            <p className="text-sm text-slate-600">Could not load shipments.</p>
            <button
              type="button"
              onClick={() => refetch()}
              className="mt-3 rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white focus-visible:ring-2 focus-visible:ring-accent-500"
            >
              Retry
            </button>
          </div>
        ) : shipments.length > 0 ? (
          <>
            {/* Desktop Table View */}
            <div className="shipment-table overflow-x-auto">
              <table className="w-full min-w-[900px] text-left text-sm" role="grid" aria-label="Shipments list">
                <thead>
                  <tr className="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-500">
                    <th className="pb-3 pr-4 font-semibold" scope="col">Risk</th>
                    <th className="pb-3 pr-4 font-semibold" scope="col">Tracking</th>
                    <th className="pb-3 pr-4 font-semibold" scope="col">Route</th>
                    <th className="pb-3 pr-4 font-semibold" scope="col">Carrier</th>
                    <th className="pb-3 pr-4 font-semibold" scope="col">Mode</th>
                    <th className="pb-3 pr-4 font-semibold" scope="col">ETA</th>
                    <th className="pb-3 font-semibold" scope="col">Status</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {shipments
                    .filter((s) => !mode || s.mode === mode)
                    .map((s) => (
                      <tr key={s.id} className="hover:bg-slate-50">
                        <td className="py-3 pr-4">
                          <RiskTooltip score={s.riskScore ?? 0} breakdown={riskBreakdown(s)}>
                            <RiskBadge
                              tier={s.riskTier as 'critical' | 'at_risk' | 'watch' | 'clear' ?? 'clear'}
                              score={s.riskScore ?? 0}
                            />
                          </RiskTooltip>
                          {s.staleHours && s.staleHours > 24 && (
                            <div className="mt-0.5 text-[10px] font-semibold text-orange-700" aria-label={`Silent for ${Math.round(s.staleHours)} hours`}>
                              silent {Math.round(s.staleHours)}h
                            </div>
                          )}
                          {s.openAlerts && s.openAlerts > 0 && !s.customerNotified && (
                            <div className="text-[10px] text-slate-600" aria-label="Customer not notified">customer not told</div>
                          )}
                        </td>
                        <td className="py-3 pr-4">
                          <Link
                            href={`/shipments/${s.id}`}
                            className="font-mono font-semibold text-navy-950 hover:text-accent-500 focus-visible:ring-2 focus-visible:ring-accent-500 rounded"
                          >
                            {s.trackingNumber}
                          </Link>
                          {s.reference && <div className="text-xs text-slate-500">{s.reference}</div>}
                        </td>
                        <td className="py-3 pr-4 text-slate-600">{s.origin} → {s.destination}</td>
                        <td className="py-3 pr-4 capitalize text-slate-600">{s.carrier}</td>
                        <td className="py-3 pr-4 capitalize text-slate-600">{s.mode}</td>
                        <td className="py-3 pr-4 font-mono text-xs text-slate-600">
                          {s.eta ? (
                            <>
                              {new Date(s.eta).toLocaleString()}
                              {s.etaSource && s.etaSource !== 'carrier' && (
                                <span className="ml-1 font-sans text-[10px] font-bold uppercase text-amber-700" aria-label="Estimated ETA">
                                  est
                                </span>
                              )}
                            </>
                          ) : (
                            <span aria-label="No ETA available">—</span>
                          )}
                        </td>
                        <td className="py-3">
                          <StatusPill status={s.status} />
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>

            {/* Mobile Card View */}
            <div className="shipment-cards space-y-3" role="list" aria-label="Shipments list">
              {shipments
                .filter((s) => !mode || s.mode === mode)
                .map((s) => (
                  <article key={s.id} className="shipment-card" role="listitem">
                    <div className="shipment-card-header">
                      <div>
                        <Link
                          href={`/shipments/${s.id}`}
                          className="shipment-card-tracking focus-visible:ring-2 focus-visible:ring-accent-500 rounded"
                        >
                          {s.trackingNumber}
                        </Link>
                        {s.reference && <div className="text-xs text-slate-400">{s.reference}</div>}
                      </div>
<RiskTooltip score={s.riskScore ?? 0} breakdown={riskBreakdown(s)}>
                        <RiskBadge
                          tier={s.riskTier as 'critical' | 'at_risk' | 'watch' | 'clear' ?? 'clear'}
                          score={s.riskScore ?? 0}
                        />
                      </RiskTooltip>
                    </div>
                    <div className="shipment-card-route">{s.origin} → {s.destination}</div>
                    <div className="shipment-card-meta">
                      <span className="shipment-card-meta-item">
                        <span className="font-medium">{s.carrier}</span>
                      </span>
                      <span className="shipment-card-meta-item">
                        <span className="font-medium capitalize">{s.mode}</span>
                      </span>
                      <span className="shipment-card-meta-item">
                        <StatusPill status={s.status} />
                      </span>
                    </div>
                    {s.eta && (
                      <div className="shipment-card-eta">
                        Estimated arrival: <span className="shipment-card-eta-value">{new Date(s.eta).toLocaleString()}</span>
                        {s.etaSource && s.etaSource !== 'carrier' && (
                          <span className="ml-1 font-sans text-[10px] font-bold uppercase text-amber-700" aria-label="Estimated ETA">est</span>
                        )}
                      </div>
                    )}
                    {s.staleHours && s.staleHours > 24 && (
                      <div className="mt-2 text-[11px] font-semibold text-orange-700" aria-label={`Silent for ${Math.round(s.staleHours)} hours`}>
                        Silent {Math.round(s.staleHours)}h
                      </div>
                    )}
                    {s.openAlerts && s.openAlerts > 0 && !s.customerNotified && (
                      <div className="mt-1 text-xs text-slate-600" aria-label="Customer not notified">Customer not notified</div>
                    )}
                    <div className="shipment-card-actions">
                      <Link
                        href={`/shipments/${s.id}`}
                        className="rounded-xl bg-accent-500 px-4 py-2 text-sm font-semibold text-white hover:bg-accent-600 focus-visible:ring-2 focus-visible:ring-accent-500"
                      >
                        View details
                      </Link>
                    </div>
                  </article>
                ))}
            </div>
          </>
        ) : (
          <Empty message="No shipments match this filter." />
        )}
      </Card>

      <nav className="mt-4 flex items-center justify-between" aria-label="Pagination">
        <button
          type="button"
          disabled={page === 0}
          onClick={() => setPage((p) => Math.max(0, p - 1))}
          className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold disabled:opacity-40 focus-visible:ring-2 focus-visible:ring-accent-500"
          aria-label="Previous page"
        >
          ← Prev
        </button>
        <span className="text-xs text-slate-500" aria-current="page">Page {page + 1} of {pages}</span>
        <button
          type="button"
          disabled={page + 1 >= pages}
          onClick={() => setPage((p) => p + 1)}
          className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold disabled:opacity-40 focus-visible:ring-2 focus-visible:ring-accent-500"
          aria-label="Next page"
        >
          Next →
        </button>
      </nav>
    </div>
  );
}