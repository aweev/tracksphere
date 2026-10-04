'use client';

import dynamic from 'next/dynamic';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { use, useState } from 'react';
import { RequireAuth } from '@/components/RequireAuth';
import { BackLink, Card, Empty, StatusPill } from '@/components/ui';
import { Row, Timeline } from '@/components/Timeline';
import type { MapPoint } from '@/components/ShipmentMap';
import { api, type AuditItem, type DocItem, type Leg, type Shipment, type ShipmentEvent } from '@/lib/api';
import { relativeTime } from '@/lib/format';

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
  const queryClient = useQueryClient();
  const [copied, setCopied] = useState(false);
  const { data: shipment, error } = useQuery({
    queryKey: ['shipment', id],
    queryFn: async () => (await api.get<Shipment>(`/api/v1/shipments/${id}`)).data,
  });
  const { data: events } = useQuery({
    queryKey: ['shipment', id, 'events'],
    queryFn: async () => (await api.get<ShipmentEvent[]>(`/api/v1/shipments/${id}/events`)).data,
  });
  const toggleVisibility = useMutation({
    mutationFn: async (next: boolean) =>
      (await api.patch<Shipment>(`/api/v1/shipments/${id}`, { isPublic: next })).data,
    onSuccess: (updated) => {
      queryClient.setQueryData(['shipment', id], updated);
    },
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

  const publicUrl =
    typeof window !== 'undefined' && shipment
      ? `${window.location.origin}/track/${encodeURIComponent(shipment.trackingNumber)}`
      : '';

  const copyLink = async () => {
    try {
      await navigator.clipboard.writeText(publicUrl);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard unavailable */
    }
  };

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
          <p className="mt-1 font-mono text-xs text-slate-400">
            updated {relativeTime(shipment.updatedAt)} · {(events ?? []).length} checkpoints
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
              <div className="text-xs text-slate-500">{relativeTime(shipment.eta)}</div>
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
            <div className="mt-4 flex flex-wrap items-center gap-2">
              <button
                type="button"
                disabled={toggleVisibility.isPending}
                onClick={() => toggleVisibility.mutate(!shipment.isPublic)}
                aria-pressed={shipment.isPublic}
                className="rounded-xl bg-navy-950 px-3 py-2 text-xs font-semibold text-white disabled:opacity-50"
              >
                {toggleVisibility.isPending
                  ? 'Saving…'
                  : shipment.isPublic
                    ? 'Make private'
                    : 'Publish to portal'}
              </button>
              {shipment.isPublic ? (
                <button
                  type="button"
                  onClick={copyLink}
                  className="rounded-xl border border-slate-200 px-3 py-2 text-xs font-semibold text-navy-950"
                >
                  {copied ? 'Copied!' : 'Copy tracking link'}
                </button>
              ) : null}
            </div>
            {toggleVisibility.isError ? (
              <p role="alert" className="mt-2 text-xs text-red-600">
                Could not update visibility. Try again.
              </p>
            ) : null}
            <p className="mt-2 text-xs text-slate-500">
              New shipments are private by default — publish only what customers should see.
            </p>
          </Card>
          <Card title="Live feed">
            <p className="text-sm text-slate-600">
              Carrier updates arrive here in real time — no refresh needed.
            </p>
            <p className="mt-2 font-mono text-xs text-slate-400">
              SSE: /api/v1/stream · tenant-scoped
            </p>
          </Card>
          <LegsCard shipmentId={id} />
          <DocumentsCard shipmentId={id} />
          <AuditCard shipmentId={id} />
        </div>
      </div>
    </div>
  );
}

function LegsCard({ shipmentId }: { shipmentId: string }) {
  const qc = useQueryClient();
  const { data: legs } = useQuery({
    queryKey: ['shipment', shipmentId, 'legs'],
    queryFn: async () => (await api.get<Leg[]>(`/api/v1/shipments/${shipmentId}/legs`)).data,
  });
  const [form, setForm] = useState({ carrier: '', mode: 'ocean', origin: '', destination: '' });
  const create = useMutation({
    mutationFn: () => api.post(`/api/v1/shipments/${shipmentId}/legs`, form),
    onSuccess: () => {
      setForm({ carrier: '', mode: 'ocean', origin: '', destination: '' });
      void qc.invalidateQueries({ queryKey: ['shipment', shipmentId, 'legs'] });
    },
  });
  const remove = useMutation({
    mutationFn: (legId: string) => api.del(`/api/v1/legs/${legId}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['shipment', shipmentId, 'legs'] }),
  });
  return (
    <Card title={`Journey legs (${legs?.length ?? 0})`}>
      {(legs ?? []).length > 0 ? (
        <ol className="mb-3 space-y-2">
          {(legs ?? []).map((l, i) => (
            <li key={l.id} className="flex items-center justify-between gap-2 text-sm">
              <span>
                <span className="font-mono text-xs text-slate-400">{i + 1}.</span>{' '}
                <span className="font-semibold">{l.origin || '?'} → {l.destination || '?'}</span>
                <span className="ml-1 text-xs capitalize text-slate-500">{l.carrier} · {l.mode} · {l.status.replace('_', ' ')}</span>
              </span>
              <button type="button" onClick={() => remove.mutate(l.id)}
                aria-label={`Remove leg ${i + 1}`}
                className="text-xs font-semibold text-red-500 hover:underline">✕</button>
            </li>
          ))}
        </ol>
      ) : (
        <p className="mb-3 text-xs text-slate-500">Single-leg shipment. Add ocean + drayage + customs legs here.</p>
      )}
      <form
        onSubmit={(e) => { e.preventDefault(); create.mutate(); }}
        className="grid grid-cols-2 gap-2"
      >
        <input value={form.origin} onChange={(e) => setForm({ ...form, origin: e.target.value })}
          placeholder="From" aria-label="Leg origin" required
          className="rounded-lg border border-slate-200 px-2 py-1.5 text-xs" />
        <input value={form.destination} onChange={(e) => setForm({ ...form, destination: e.target.value })}
          placeholder="To" aria-label="Leg destination" required
          className="rounded-lg border border-slate-200 px-2 py-1.5 text-xs" />
        <input value={form.carrier} onChange={(e) => setForm({ ...form, carrier: e.target.value })}
          placeholder="Carrier" aria-label="Leg carrier"
          className="rounded-lg border border-slate-200 px-2 py-1.5 text-xs" />
        <select value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value })}
          aria-label="Leg mode" className="rounded-lg border border-slate-200 px-2 py-1.5 text-xs">
          <option value="ocean">Ocean</option>
          <option value="air">Air</option>
          <option value="road">Road</option>
          <option value="rail">Rail</option>
        </select>
        <button type="submit" className="col-span-2 rounded-lg bg-navy-950 px-3 py-1.5 text-xs font-semibold text-white">
          Add leg
        </button>
      </form>
    </Card>
  );
}

function DocumentsCard({ shipmentId }: { shipmentId: string }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const { data: docs } = useQuery({
    queryKey: ['shipment', shipmentId, 'docs'],
    queryFn: async () => (await api.get<DocItem[]>(`/api/v1/shipments/${shipmentId}/documents`)).data,
  });
  const upload = async (file: File) => {
    setBusy(true);
    setError('');
    try {
      const fd = new FormData();
      fd.append('file', file);
      const res = await fetch(`/api/v1/shipments/${shipmentId}/documents`, { method: 'POST', body: fd, credentials: 'include' });
      if (!res.ok) {
        const body = await res.json().catch(() => null);
        throw new Error(body?.error?.message ?? 'Upload failed');
      }
      void qc.invalidateQueries({ queryKey: ['shipment', shipmentId, 'docs'] });
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Upload failed');
    } finally {
      setBusy(false);
    }
  };
  const remove = useMutation({
    mutationFn: (docId: string) => api.del(`/api/v1/documents/${docId}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['shipment', shipmentId, 'docs'] }),
  });
  return (
    <Card title={`Documents (${docs?.length ?? 0})`}>
      <label className="mb-2 block cursor-pointer rounded-xl border border-dashed border-slate-300 p-3 text-center text-xs text-slate-500 hover:border-accent-500">
        {busy ? 'Uploading…' : 'BOL, POD photo, customs paperwork (PDF/PNG/JPG, 10 MiB)'}
        <input
          type="file" className="hidden" disabled={busy}
          accept=".pdf,.png,.jpg,.jpeg,.webp,.txt,.csv"
          onChange={(e) => { const f = e.target.files?.[0]; if (f) void upload(f); e.target.value = ''; }}
        />
      </label>
      {error ? <p role="alert" className="mb-2 text-xs text-red-600">{error}</p> : null}
      {(docs ?? []).length === 0 ? (
        <p className="text-xs text-slate-400">No documents yet.</p>
      ) : (
        <ul className="space-y-1.5">
          {(docs ?? []).map((d) => (
            <li key={d.id} className="flex items-center justify-between gap-2 text-xs">
              <a href={`/api/v1/documents/${d.id}/download`}
                className="truncate font-mono text-navy-950 hover:underline" title={d.filename}>
                {d.filename}
              </a>
              <button type="button" onClick={() => remove.mutate(d.id)}
                aria-label={`Delete ${d.filename}`}
                className="shrink-0 font-semibold text-red-500 hover:underline">✕</button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function AuditCard({ shipmentId }: { shipmentId: string }) {
  const { data: items } = useQuery({
    queryKey: ['shipment', shipmentId, 'audit'],
    queryFn: async () => (await api.get<AuditItem[]>(`/api/v1/shipments/${shipmentId}/audit`)).data,
  });
  if (!items || items.length === 0) return null;
  return (
    <Card title="Activity">
      <ul className="space-y-2">
        {items.slice(0, 8).map((a, i) => (
          <li key={i} className="text-xs">
            <span className="font-semibold text-navy-950">{a.action}</span>
            <span className="text-slate-500"> · {a.actor || 'system'} · {relativeTime(a.createdAt)}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}
