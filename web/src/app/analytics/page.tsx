'use client';

import { useQuery } from '@tanstack/react-query';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import { api, type Analytics, type Digest } from '@/lib/api';

function Bar({ pct }: { pct: number }) {
  const v = Math.max(0, Math.min(100, Math.round(pct)));
  return (
    <div className="h-2 rounded-full bg-slate-100" role="progressbar"
      aria-valuenow={v} aria-valuemin={0} aria-valuemax={100}>
      <div className={`h-2 rounded-full ${v >= 90 ? 'bg-emerald-500' : v >= 70 ? 'bg-accent-500' : 'bg-red-500'}`}
        style={{ width: `${v}%` }} />
    </div>
  );
}

export default function AnalyticsPage() {
  return (
    <RequireAuth>
      <AnalyticsBody />
    </RequireAuth>
  );
}

function AnalyticsBody() {
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['analytics'],
    queryFn: async () => (await api.get<Analytics>('/api/v1/analytics')).data,
  });

  const exportCsv = () => {
    if (!data) return;
    const rows = [['carrier', 'total', 'onTime', 'onTimePct'],
      ...data.carriers.map((c) => [c.carrier, c.total, c.onTime, c.onTimePct?.toFixed(1) ?? ''])];
    const csv = rows.map((r) => r.join(',')).join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'carrier-benchmark.csv';
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="space-y-6 p-4 md:p-8">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-extrabold text-navy-950">Analytics</h1>
          <p className="text-sm text-slate-500">On-time, carriers, lanes — Postgres-native (ClickHouse at scale)</p>
        </div>
        <button type="button" onClick={exportCsv}
          className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold">
          Export carriers CSV
        </button>
      </header>

      {isLoading ? (
        <Card title="Loading…"><div className="h-24 animate-pulse rounded-xl bg-slate-100" /></Card>
      ) : error || !data ? (
        <Card title="Unavailable">
          <p className="text-sm text-slate-600">Could not load analytics.</p>
          <button type="button" onClick={() => refetch()}
            className="mt-3 rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">Retry</button>
        </Card>
      ) : data.total === 0 ? (
        <Empty message="No shipments yet — analytics appear once you track freight." />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
            <Card title="On-time %">
              <div className="text-3xl font-extrabold text-navy-950">
                {data.onTimePct !== undefined ? `${data.onTimePct.toFixed(1)}%` : '—'}
              </div>
              <div className="mt-1 text-xs text-slate-500">{data.onTime} of {data.delivered} delivered on time</div>
            </Card>
            <Card title="ETA ±6h">
              <div className="text-3xl font-extrabold text-navy-950">
                {data.etaAccuracyPct != null ? `${data.etaAccuracyPct.toFixed(1)}%` : '—'}
              </div>
              <div className="mt-1 text-xs text-slate-500">
                {data.etaSamples ?? 0} ETAs scored · lane-learned
              </div>
            </Card>
            <Card title="Avg transit">
              <div className="text-3xl font-extrabold text-navy-950">
                {data.avgTransitDays != null ? `${data.avgTransitDays.toFixed(1)}d` : '—'}
              </div>
              <div className="mt-1 text-xs text-slate-500">booked → delivered</div>
            </Card>
            <Card title="Exceptions">
              <div className="text-3xl font-extrabold text-red-600">{data.exceptions}</div>
              <div className="mt-1 text-xs text-slate-500">open + history in queue</div>
            </Card>
            <Card title="Tracked">
              <div className="text-3xl font-extrabold text-navy-950">{data.total}</div>
              <div className="mt-1 text-xs text-slate-500">shipments all time</div>
            </Card>
          </div>

          <Card title="Carrier benchmark">
            {data.carriers.length === 0 ? <Empty message="No carrier data yet." /> : (
              <ul className="space-y-3">
                {data.carriers.map((c) => (
                  <li key={c.carrier}>
                    <div className="flex justify-between text-sm">
                      <span className="font-semibold capitalize">{c.carrier}</span>
                      <span className="font-mono text-xs text-slate-500">
                        {c.total} ships · {c.onTimePct != null ? `${c.onTimePct.toFixed(0)}% OT` : '—'}
                        {c.avgTransitDays != null ? ` · ${c.avgTransitDays.toFixed(1)}d avg` : ''}
                      </span>
                    </div>
                    <div className="mt-1"><Bar pct={c.onTimePct ?? 0} /></div>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Top lanes">
            {data.lanes.length === 0 ? <Empty message="No lane data yet." /> : (
              <ul className="divide-y divide-slate-100">
                {data.lanes.map((l) => (
                  <li key={`${l.origin}|${l.destination}`} className="flex justify-between py-2.5 text-sm">
                    <span>{l.origin || '?'} → {l.destination || '?'}</span>
                    <span className="font-mono text-xs text-slate-500">
                      {l.total} ships{l.exceptions > 0 ? ` · ${l.exceptions} exceptions` : ''}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          <DigestCard />
        </>
      )}
    </div>
  );
}

function DigestCard() {
  const { data } = useQuery({
    queryKey: ['digest'],
    queryFn: async () => (await api.get<Digest>('/api/v1/analytics/digest')).data,
  });
  if (!data) return null;
  return (
    <Card title="This week" action={
      <span className="text-xs text-slate-400">auto-emailed to owners Mondays</span>
    }>
<div className="flex flex-wrap gap-x-6 gap-y-2 text-sm">
        <span><strong className="font-extrabold">{data.delivered}</strong> delivered</span>
        <span><strong className="font-extrabold">{data.onTimePct != null ? `${data.onTimePct.toFixed(0)}%` : '—'}</strong> on-time</span>
        <span><strong className="font-extrabold">{data.exceptions}</strong> exceptions</span>
        <span><strong className="font-extrabold">{data.openAlerts}</strong> open alerts</span>
        <span><strong className="font-extrabold">{data.staleShipments}</strong> stale {">"}48h</span>
      </div>
      {data.topCarriers.length > 0 ? (
        <p className="mt-2 text-xs text-slate-500">
          Top: {data.topCarriers.map((c) => `${c.carrier} (${c.total})`).join(' · ')}
        </p>
      ) : null}
    </Card>
  );
}
