'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import { api } from '@/lib/api';

interface Cred {
  id: string;
  carrier: string;
  baseUrl: string;
  pollMinutes: number;
  active: boolean;
  lastPolledAt?: string;
  lastError?: string;
}

interface Commerce {
  id: string;
  provider: string;
  shopUrl: string;
  active: boolean;
}

export default function IntegrationsPage() {
  return (
    <RequireAuth>
      <div className="space-y-6 p-4 md:p-8">
        <header>
          <h1 className="text-2xl font-extrabold text-navy-950">Integrations</h1>
          <p className="text-sm text-slate-500">Carriers that poll themselves + stores that create shipments</p>
        </header>
        <CarriersCard />
        <CommerceCard />
      </div>
    </RequireAuth>
  );
}

function CarriersCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['carriers'],
    queryFn: async () => (await api.get<{ supported: string[]; credentials: Cred[] }>('/api/v1/carriers')).data,
  });
  const [carrier, setCarrier] = useState('maersk');
  const [baseUrl, setBaseUrl] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [msg, setMsg] = useState('');
  const save = useMutation({
    mutationFn: () => api.post('/api/v1/carriers', { carrier, baseUrl, apiKey }),
    onSuccess: () => { setMsg('Saved. Polling starts within a minute.'); setApiKey(''); void qc.invalidateQueries({ queryKey: ['carriers'] }); },
    onError: (e: unknown) => setMsg(e instanceof Error ? e.message : 'Save failed.'),
  });
  const poll = useMutation({
    mutationFn: (c: string) => api.post(`/api/v1/carriers/${c}/poll`),
    onSuccess: () => setMsg('Poll queued — checkpoints land in seconds.'),
    onError: (e: unknown) => setMsg(e instanceof Error ? e.message : 'Poll failed.'),
  });
  const remove = useMutation({
    mutationFn: (c: string) => api.del(`/api/v1/carriers/${c}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['carriers'] }),
  });
  return (
    <Card title="Carrier polling (no webhooks needed)">
      <form onSubmit={(e: FormEvent) => { e.preventDefault(); setMsg(''); save.mutate(); }}
        className="mb-4 flex flex-wrap gap-2">
        <select value={carrier} onChange={(e) => setCarrier(e.target.value)} aria-label="Carrier"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm">
          {(data?.supported ?? ['maersk', 'dhl', 'fedex', 'ups', 'fake']).map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)}
          placeholder="Base URL (fake://… for the demo carrier)" aria-label="Base URL"
          className="min-w-52 flex-1 rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <input value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder="API key (optional)"
          aria-label="API key" autoComplete="off"
          className="rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <button type="submit" className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          Save
        </button>
      </form>
      {msg ? <p role="status" className="mb-3 text-sm text-slate-600">{msg}</p> : null}
      {(data?.credentials ?? []).length === 0 ? <Empty message="No polling carriers yet." /> : (
        <ul className="divide-y divide-slate-100">
          {(data?.credentials ?? []).map((c) => (
            <li key={c.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
              <div>
                <span className="font-semibold capitalize">{c.carrier}</span>
                <span className="ml-2 font-mono text-xs text-slate-500">{c.baseUrl || '(no URL)'}</span>
                <span className="ml-2 text-xs text-slate-400">
                  every {c.pollMinutes}m{c.lastPolledAt ? ` · polled ${new Date(c.lastPolledAt).toLocaleString()}` : ' · never polled'}
                </span>
                {c.lastError ? <div className="text-xs text-red-600">{c.lastError}</div> : null}
              </div>
              <div className="flex gap-2">
                <button type="button" onClick={() => poll.mutate(c.carrier)}
                  className="rounded-lg border border-slate-200 px-3 py-1.5 text-xs font-semibold">
                  Poll now
                </button>
                <button type="button" onClick={() => remove.mutate(c.carrier)}
                  className="rounded-lg border border-red-200 px-3 py-1.5 text-xs font-semibold text-red-600">
                  Remove
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function CommerceCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['commerce'],
    queryFn: async () => (await api.get<Commerce[]>('/api/v1/integrations/commerce')).data,
  });
  const [provider, setProvider] = useState('shopify');
  const [shopUrl, setShopUrl] = useState('');
  const [token, setToken] = useState('');
  const [once, setOnce] = useState('');
  const connect = useMutation({
    mutationFn: () => api.post<{ webhookSecret: string }>('/api/v1/integrations/commerce', { provider, shopUrl, token }),
    onSuccess: (r) => {
      setOnce(`Webhook secret: ${r.data.webhookSecret} — paste it in the store's webhook settings.`);
      setToken('');
      void qc.invalidateQueries({ queryKey: ['commerce'] });
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/integrations/commerce/${id}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['commerce'] }),
  });
  return (
    <Card title="Stores (Shopify / WooCommerce)">
      <p className="mb-3 text-xs text-slate-500">
        Order webhooks with fulfillments auto-create shipments. Point the store at
        <span className="font-mono"> POST /api/v1/webhooks/commerce/{'{provider}'}</span> with the secret below.
      </p>
      <form onSubmit={(e: FormEvent) => { e.preventDefault(); connect.mutate(); }}
        className="mb-3 flex flex-wrap gap-2">
        <select value={provider} onChange={(e) => setProvider(e.target.value)} aria-label="Provider"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm">
          <option value="shopify">Shopify</option>
          <option value="woocommerce">WooCommerce</option>
        </select>
        <input value={shopUrl} onChange={(e) => setShopUrl(e.target.value)} placeholder="mystore.myshopify.com"
          aria-label="Shop URL" className="rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <input value={token} onChange={(e) => setToken(e.target.value)} placeholder="Admin token (optional)"
          aria-label="Token" autoComplete="off"
          className="rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <button type="submit" className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          Connect
        </button>
      </form>
      {once ? (
        <p role="status" className="mb-3 break-all rounded-xl bg-amber-50 p-3 font-mono text-xs text-amber-800">
          {once}
          <button type="button" onClick={() => setOnce('')} className="ml-2 font-sans font-semibold underline">clear</button>
        </p>
      ) : null}
      {(data ?? []).length === 0 ? <Empty message="No stores connected." /> : (
        <ul className="divide-y divide-slate-100">
          {(data ?? []).map((c) => (
            <li key={c.id} className="flex items-center justify-between py-2.5 text-sm">
              <span><span className="font-semibold capitalize">{c.provider}</span>
                <span className="ml-2 font-mono text-xs text-slate-500">{c.shopUrl}</span></span>
              <button type="button" onClick={() => remove.mutate(c.id)}
                className="rounded-lg border border-red-200 px-3 py-1.5 text-xs font-semibold text-red-600">
                Disconnect
              </button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
