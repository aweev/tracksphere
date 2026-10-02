'use client';

import dynamic from 'next/dynamic';
import { useQuery } from '@tanstack/react-query';
import { use } from 'react';
import { RequireAuth } from '@/components/RequireAuth';
import { BackLink, Card, Empty, StatusPill } from '@/components/ui';
import { Row, Timeline } from '@/components/Timeline';
import type { MapPoint } from '@/components/ShipmentMap';
import { api, type Shipment, type ShipmentEvent } from '@/lib/api';

// MapLibre touches window at import → client-only, no SSR.
const ShipmentMap = dynamic(() => import('@/components/ShipmentMap'), {
  ssr: false,
  loading: () => (
    <div className="flex h-72 items-center justify-center rounded-2xl bg-slate-100 text-sm text-slate-400">
      Loading map…
    </div>
  ),
});

export default function ShipmentDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <RequireAuth>
      <ShipmentDetailBody id={id} />
    </RequireAuth>
  );
}

function mapPoints(events: ShipmentEvent[]): MapPoint[] {
  return events
    .filter((e) => e.lat !== undefined && e.lng !== undefined)
    .map((e) => ({
      lng: e.lng as number,
      lat: e.lat as number,
      label: `${e.code} — ${e.location || 'unknown'}`,
      kind: 'event' as const,
    }));
}

function ShipmentDetailBody({ id }: { id: string }) {
  const { data: shipment, error } = useQuery({
    queryKey: ['shipment', id],
    queryFn: async () => (await api.get<Shipment>(`/api/v1/shipments/${id}`)).data,
  });
  const { data: events } = useQuery({
    queryKey: ['shipment', id, 'events'],
    queryFn: async () => (await api.get<ShipmentEvent[]>(`/api/v1/shipments/${id}/events`)).data,
  });

  if (error) {
    return (
      <div className="p-8">
        <Empty message="Shipment not found (or not visible to your organization)." />
        <div className="mt-4">
          <BackLink href="/shipments">← Back to shipments</BackLink>
        </div>
      </div>
    );
  }
  if (!shipment) return <div className="p-8 text-slate-400">Loading…</div>;
  const points = mapPoints(events ?? []);

  return (
    <div className="p-8">
      <BackLink href="/shipments">← Shipments</BackLink>
      <header className="mt-3 mb-6 flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="font-mono text-2xl font-extrabold text-navy-950">
            {shipment.trackingNumber}
          </h1>
          <p className="text-sm text-slate-500">
            {shipment.origin} → {shipment.destination} · {shipment.carrier} · {shipment.mode}
            {shipment.reference ? ` · ${shipment.reference}` : ''}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <StatusPill status={shipment.status} />
          {shipment.eta ? (
            <div className="text-right">
              <div className="text-xs uppercase tracking-wide text-slate-400">ETA</div>
              <div className="font-mono text-sm font-semibold text-navy-950">
                {new Date(shipment.eta).toLocaleString()}
              </div>
            </div>
          ) : null}
        </div>
      </header>

      <div className="grid gap-6 lg:grid-cols-3">
        <div className="space-y-6 lg:col-span-2">
          <Card title="Route map">
            <ShipmentMap points={points} />
          </Card>
          <Card title="Timeline">
            {events && events.length > 0 ? (
              <Timeline events={events} />
            ) : (
              <Empty message="No events yet." />
            )}
          </Card>
        </div>

        <div className="space-y-6">
          <Card title="Details">
            <dl className="space-y-3 text-sm">
              <Row label="Status" value={shipment.status.replace('_', ' ')} />
              <Row label="Carrier" value={shipment.carrier} />
              <Row label="Mode" value={shipment.mode} />
              <Row label="Created" value={new Date(shipment.createdAt).toLocaleString()} />
              <Row label="Updated" value={new Date(shipment.updatedAt).toLocaleString()} />
              <Row label="Public portal" value={shipment.isPublic ? 'Visible' : 'Hidden'} />
            </dl>
          </Card>
          <Card title="Live feed">
            <p className="text-sm text-slate-600">
              Carrier updates arrive here in real time — no refresh needed.
            </p>
            <p className="mt-2 font-mono text-xs text-slate-400">
              SSE: /api/v1/stream · tenant-scoped
            </p>
          </Card>
        </div>
      </div>
    </div>
  );
}
