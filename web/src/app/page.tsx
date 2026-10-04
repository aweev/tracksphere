'use client';

import { useQuery } from '@tanstack/react-query';
import dynamic from 'next/dynamic';
import Link from 'next/link';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty, StatTile, StatusPill } from '@/components/ui';
import { api, type Alert, type DashboardStats, type Shipment } from '@/lib/api';

// maplibre-gl is ~150 kB and touches `window` at import. Loading it eagerly
// put the whole library in the dashboard's first-load JS for every user,
// including the majority who never scroll to the map. Same treatment as the
// shipment detail map.
const FleetMap = dynamic(() => import('@/components/FleetMap'), {
  ssr: false,
  loading: () => (
    <div className="flex h-64 items-center justify-center rounded-2xl bg-slate-50 text-sm text-slate-400">
      Loading fleet map…
    </div>
  ),
});

const RISK_LABELS: Record<string, string> = {
  clear: 'On track',
  watch: 'Watch',
  at_risk: 'At risk',
  critical: 'Critical',
};

const RISK_STYLES: Record<string, string> = {
  clear: 'bg-emerald-100 text-emerald-800',
  watch: 'bg-amber-100 text-amber-800',
  at_risk: 'bg-orange-100 text-orange-800',
  critical: 'bg-red-100 text-red-700',
};

export default function DashboardPage() {
  return (
    <RequireAuth>
      <DashboardBody />
    </RequireAuth>
  );
}

function DashboardBody() {
  const { data: stats } = useQuery({
    queryKey: ['dashboard'],
    queryFn: async () => (await api.get<DashboardStats>('/api/v1/dashboard')).data,
  });
  // Sorted by risk, not by status: ops want a to-do list ordered by
  // consequence, not a database view.
  const { data: risky } = useQuery({
    queryKey: ['shipments', 'risk', 'recent'],
    queryFn: async () =>
      (await api.get<Shipment[]>('/api/v1/shipments?limit=10&sort=risk')).data,
  });
  const { data: mapped } = useQuery({
    queryKey: ['shipments', 'map', 'points'],
    queryFn: async () =>
      (await api.get<Shipment[]>('/api/v1/shipments?limit=250&hasPosition=true')).data,
  });
  const { data: alerts } = useQuery({
    queryKey: ['alerts', 'open'],
    queryFn: async () => (await api.get<Alert[]>('/api/v1/alerts?status=open')).data,
  });

  const critical = (alerts ?? []).filter((a) => a.severity === 'critical');

  return (
    <div className="p-4 md:p-8">
      <header className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-extrabold text-navy-950">Operations overview</h1>
          <p className="text-sm text-slate-500">
            Live view across every active shipment
          </p>
        </div>
        <Link
          href="/exceptions"
          className="rounded-xl bg-accent-500 px-4 py-2.5 text-sm font-semibold text-white hover:bg-accent-600"
        >
          Work exceptions →
        </Link>
      </header>

      {/* KPI row. Counts alone are not information — each tile carries the
          action it implies. */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
        <StatTile
          label="Active"
          value={stats?.activeShipments ?? 0}
          href="/shipments"
        />
        <StatTile
          label="Delivered"
          value={stats?.deliveredShipments ?? 0}
          tone="success"
          href="/shipments?status=delivered"
        />
        <StatTile
          label="Exceptions"
          value={stats?.exceptionShipments ?? 0}
          tone="danger"
          href="/shipments?status=exception"
        />
        <StatTile
          label="Past ETA"
          value={stats?.overdueShipments ?? 0}
          tone="accent"
          href="/shipments"
        />
        <StatTile
          label="Open exceptions"
          value={stats?.openAlerts ?? 0}
          tone="danger"
          href="/exceptions"
        />
      </div>

      {/* The ambient layer: where everything is, right now. This was the
          defining control-tower gesture and it was missing entirely. */}
      <div className="mt-6">
        <Card
          title="Live fleet"
          action={
            <Link
              href="/shipments"
              className="text-xs font-semibold text-accent-500 hover:underline"
            >
              All shipments →
            </Link>
          }
        >
          <FleetMap shipments={mapped ?? []} />
        </Card>
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <Card title="Needs attention first">
            {risky && risky.length > 0 ? (
              <ul className="divide-y divide-slate-100">
                {risky.map((s) => {
                  const tier = s.riskTier ?? 'clear';
                  return (
                    <li key={s.id}>
                      <Link
                        href={`/shipments/${s.id}`}
                        className="flex items-center justify-between gap-3 py-3 hover:bg-slate-50"
                      >
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="font-mono text-sm font-semibold text-navy-950">
                              {s.trackingNumber}
                            </span>
                            {tier !== 'clear' ? (
                              <span
                                className={`rounded-full px-2 py-0.5 text-[10px] font-bold uppercase tracking-wide ${RISK_STYLES[tier]}`}
                              >
                                {RISK_LABELS[tier]}
                                {s.riskScore ? ` · ${s.riskScore}` : ''}
                              </span>
                            ) : null}
                            {s.customerNotified ? (
                              <span className="text-[10px] font-semibold text-emerald-700">
                                Customer told
                              </span>
                            ) : tier !== 'clear' ? (
                              <span className="text-[10px] font-semibold text-slate-500">
                                Customer not told
                              </span>
                            ) : null}
                          </div>
                          <div className="text-xs text-slate-500">
                            {s.origin} → {s.destination} · {s.carrier}
                            {s.staleHours && s.staleHours > 24
                              ? ` · no scan ${Math.round(s.staleHours)}h`
                              : ''}
                            {s.valueAtRisk
                              ? ` · $${Math.round(s.valueAtRisk).toLocaleString()} at risk`
                              : ''}
                          </div>
                        </div>
                        <StatusPill status={s.status} />
                      </Link>
                    </li>
                  );
                })}
              </ul>
            ) : (
              // The first-run experience was a dashed box. It is the
              // highest-leverage screen in the product and it was giving up.
              <Empty message="No shipments yet." />
            )}
          </Card>
        </div>

        <Card
          title={
            critical.length > 0
              ? `Critical (${critical.length})`
              : `Alerts (${alerts?.length ?? 0})`
          }
          action={
            <Link
              href="/exceptions"
              className="text-xs font-semibold text-accent-500 hover:underline"
            >
              Open queue →
            </Link>
          }
        >
          {alerts && alerts.length > 0 ? (
            <ul className="space-y-3">
              {alerts.slice(0, 6).map((a) => (
                <li
                  key={a.id}
                  className={`rounded-2xl border-l-4 p-3 ${
                    a.severity === 'critical'
                      ? 'border-red-500 bg-red-50'
                      : a.severity === 'warning'
                        ? 'border-amber-500 bg-amber-50'
                        : 'border-blue-500 bg-blue-50'
                  }`}
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={`rounded-full px-1.5 py-0.5 text-[10px] font-bold uppercase ${
                        a.severity === 'critical'
                          ? 'bg-red-600 text-white'
                          : a.severity === 'warning'
                            ? 'bg-amber-500 text-white'
                            : 'bg-blue-500 text-white'
                      }`}
                    >
                      {a.severity}
                    </span>
                    {a.assignedToName ? (
                      <span className="text-[10px] font-semibold text-slate-600">
                        {a.assignedToName}
                      </span>
                    ) : (
                      <span className="text-[10px] font-semibold text-orange-700">
                        Unowned
                      </span>
                    )}
                  </div>
                  <div className="mt-1 text-sm font-bold text-navy-950">{a.title}</div>
                  <div className="mt-0.5 line-clamp-2 text-xs text-slate-600">
                    {a.message}
                  </div>
                  <Link
                    href={`/shipments/${a.shipmentId}`}
                    className="mt-1 inline-block text-xs font-semibold text-accent-500 hover:underline"
                  >
                    Open shipment →
                  </Link>
                </li>
              ))}
            </ul>
          ) : (
            <Empty message="All clear — no open exceptions." />
          )}
        </Card>
      </div>
    </div>
  );
}