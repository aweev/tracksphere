'use client';

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import Link from 'next/link';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty, StatusPill } from '@/components/ui';
import { api, type Shipment, type ShipmentStatus } from '@/lib/api';
import { CreateShipmentForm } from '@/components/CreateShipmentForm';

const STATUSES: Array<ShipmentStatus | ''> = [
  '', 'booked', 'in_transit', 'at_customs', 'out_for_delivery',
  'delivered', 'exception', 'cancelled',
];

export default function ShipmentsPage() {
  return (
    <RequireAuth>
      <ShipmentsBody />
    </RequireAuth>
  );
}

function ShipmentsBody() {
  const qc = useQueryClient();
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [showCreate, setShowCreate] = useState(false);

  const { data: result } = useQuery({
    queryKey: ['shipments', { status, search }],
    queryFn: async () => {
      const params = new URLSearchParams({ limit: '50' });
      if (status) params.set('status', status);
      if (search) params.set('search', search);
      return api.get<Shipment[]>(`/api/v1/shipments?${params}`);
    },
  });
  const shipments = result?.data;
  const total = result?.meta?.total;

  return (
    <div className="p-8">
      <header className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-extrabold text-navy-950">Shipments</h1>
          <p className="text-sm text-slate-500">
            {total !== undefined ? `${total} total` : 'Loading…'}
          </p>
        </div>
        <button
          onClick={() => setShowCreate((v) => !v)}
          className="rounded-xl bg-accent-500 px-4 py-2.5 text-sm font-semibold text-white hover:bg-accent-600"
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

      <div className="mb-4 flex flex-wrap gap-3">
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search tracking # or reference…"
          className="w-72 rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        />
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          className="rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none"
        >
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {s === '' ? 'All statuses' : s.replace('_', ' ')}
            </option>
          ))}
        </select>
      </div>

      <Card>
        {shipments && shipments.length > 0 ? (
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-slate-200 text-xs uppercase tracking-wide text-slate-400">
                <th className="pb-3 pr-4 font-semibold">Tracking</th>
                <th className="pb-3 pr-4 font-semibold">Route</th>
                <th className="pb-3 pr-4 font-semibold">Carrier</th>
                <th className="pb-3 pr-4 font-semibold">Mode</th>
                <th className="pb-3 pr-4 font-semibold">ETA</th>
                <th className="pb-3 font-semibold">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {shipments.map((s) => (
                <tr key={s.id} className="hover:bg-slate-50">
                  <td className="py-3 pr-4">
                    <Link
                      href={`/shipments/${s.id}`}
                      className="font-mono font-semibold text-navy-950 hover:text-accent-500"
                    >
                      {s.trackingNumber}
                    </Link>
                    {s.reference ? <div className="text-xs text-slate-400">{s.reference}</div> : null}
                  </td>
                  <td className="py-3 pr-4 text-slate-600">
                    {s.origin} → {s.destination}
                  </td>
                  <td className="py-3 pr-4 capitalize text-slate-600">{s.carrier}</td>
                  <td className="py-3 pr-4 capitalize text-slate-600">{s.mode}</td>
                  <td className="py-3 pr-4 font-mono text-xs text-slate-500">
                    {s.eta ? new Date(s.eta).toLocaleString() : '—'}
                  </td>
                  <td className="py-3">
                    <StatusPill status={s.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <Empty message="No shipments match this filter." />
        )}
      </Card>
    </div>
  );
}
