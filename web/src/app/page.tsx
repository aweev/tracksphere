'use client';

import { useQuery } from '@tanstack/react-query';
import Link from 'next/link';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty, StatTile, StatusPill } from '@/components/ui';
import { api, type Alert, type DashboardStats, type Shipment } from '@/lib/api';

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
  const { data: shipments } = useQuery({
    queryKey: ['shipments', 'recent'],
    queryFn: async () => (await api.get<Shipment[]>('/api/v1/shipments?limit=8')).data,
  });
  const { data: alerts } = useQuery({
    queryKey: ['alerts', 'open'],
    queryFn: async () => (await api.get<Alert[]>('/api/v1/alerts?status=open')).data,
  });

  return (
    <div className="p-8">
      <header className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-extrabold text-navy-950">Operations overview</h1>
          <p className="text-sm text-slate-500">Live view across every active shipment</p>
        </div>
        <Link
          href="/shipments"
          className="rounded-xl bg-navy-950 px-4 py-2.5 text-sm font-semibold text-white hover:bg-navy-900"
        >
          All shipments →
        </Link>
      </header>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
        <StatTile label="Active" value={stats?.activeShipments ?? 0} />
        <StatTile label="Delivered" value={stats?.deliveredShipments ?? 0} tone="success" />
        <StatTile label="Exceptions" value={stats?.exceptionShipments ?? 0} tone="danger" />
        <StatTile label="Past ETA" value={stats?.overdueShipments ?? 0} tone="accent" />
        <StatTile label="Open alerts" value={stats?.openAlerts ?? 0} tone="danger" />
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <Card title="Recent shipments">
            {shipments && shipments.length > 0 ? (
              <ul className="divide-y divide-slate-100">
                {shipments.map((s) => (
                  <li key={s.id}>
                    <Link
                      href={`/shipments/${s.id}`}
                      className="flex items-center justify-between py-3 hover:bg-slate-50"
                    >
                      <div>
                        <div className="font-mono text-sm font-semibold text-navy-950">
                          {s.trackingNumber}
                        </div>
                        <div className="text-xs text-slate-500">
                          {s.origin} → {s.destination} · {s.carrier}
                        </div>
                      </div>
                      <StatusPill status={s.status} />
                    </Link>
                  </li>
                ))}
              </ul>
            ) : (
              <Empty message="No shipments yet — create your first one." />
            )}
          </Card>
        </div>

        <Card title={`Alerts (${alerts?.length ?? 0})`}>
          {alerts && alerts.length > 0 ? (
            <ul className="space-y-3">
              {alerts.map((a) => (
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
                  <div className="text-sm font-bold text-navy-950">{a.title}</div>
                  <div className="mt-0.5 line-clamp-2 text-xs text-slate-600">{a.message}</div>
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
            <Empty message="All clear — no open alerts." />
          )}
        </Card>
      </div>
    </div>
  );
}
