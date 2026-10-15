'use client';

import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import Link from 'next/link';
import { Card, Empty, StatusPill } from '@/components/ui';
import { EtaBadge } from '@/components/EtaBadge';
import { ConnectionDot } from '@/components/ConnectionDot';
import { useTrackedStream } from '@/lib/live';
import { Timeline } from '@/components/Timeline';
import { api, RequestError, type PublicTracking } from '@/lib/api';
import { relativeTime } from '@/lib/format';

/** Customer-facing tracker: no login, safe projection from the API only. */
export function PublicTrack({ initial }: { initial: string }) {
  const [input, setInput] = useState(initial);
  const [tracking, setTracking] = useState(initial);
  const qc = useQueryClient();
  const { data, error, isFetching } = useQuery({
    queryKey: ['track', tracking],
    queryFn: async () => (await api.get<PublicTracking>(`/api/v1/track/${tracking}`)).data,
    enabled: tracking.length > 0,
    retry: false,
  });

  // Live updates. The portal is the surface the customer actually watches, and
  // until this existed it was a one-shot snapshot — the most-demoed screen in
  // the product was the only one that never moved. The stream is scoped server
  // side to exactly this shipment, so an anonymous visitor cannot observe anyone
  // else's events. Tracked: connection state drives the dot, and a reconnect
  // refetches (Last-Event-ID resumes the stream; the refetch closes any gap).
  const streamUrl = tracking
    ? `/api/v1/track/${encodeURIComponent(tracking)}/stream`
    : null;
  const conn = useTrackedStream(streamUrl, {
    events: ['shipment.updated'],
    onEvent: () => {
      void qc.invalidateQueries({ queryKey: ['track', tracking] });
    },
    onReconnect: () => {
      void qc.invalidateQueries({ queryKey: ['track', tracking] });
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTracking(input.trim());
  };

  const brand = data?.brand;
  const accent = brand?.color || '#ff6b00';

  return (
    <div className="mx-auto max-w-3xl p-6">
      <header className="mb-8 pt-8 text-center">
        {brand?.company ? (
          <div className="text-2xl font-extrabold text-navy-950">
            {brand.logo ? (
              // eslint-disable-next-line @next/next/no-img-element
              <img src={brand.logo} alt={brand.company} className="mx-auto mb-2 h-10 object-contain" />
            ) : null}
            {brand.company}
          </div>
        ) : (
          <Link href="/track" className="text-2xl font-extrabold text-navy-950 focus-visible:ring-2 focus-visible:ring-accent-500 rounded">
            Track<span className="text-accent-500">Sphere</span>
          </Link>
        )}
        <p className="mt-1 text-sm text-slate-600">
          Track your shipment in real time
        </p>
        {tracking ? (
          <div className="mt-2 flex justify-center">
            <ConnectionDot state={conn} updatedAt={data?.lastUpdate} />
          </div>
        ) : null}
      </header>

      <form onSubmit={onSubmit} className="mb-8 flex gap-2" role="search">
        <label htmlFor="tracking-input" className="sr-only">
          Tracking number
        </label>
        <input
          id="tracking-input"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Enter tracking number (e.g. TS-8842-LAG)"
          className="flex-1 rounded-2xl border border-slate-300 px-5 py-3.5 font-mono focus:border-accent-500 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500"
        />
        <button
          type="submit"
          className="rounded-2xl bg-accent-700 px-6 py-3.5 font-semibold text-white hover:bg-accent-700-hover focus-visible:ring-2 focus-visible:ring-accent-500 focus-visible:ring-offset-2"
        >
          {isFetching ? '…' : 'Track'}
        </button>
      </form>

      {error ? (
        <Empty
          message={
            error instanceof RequestError && error.status === 404
              ? 'No shipment found for this tracking number.'
              : 'Tracking failed — try again.'
          }
        />
      ) : null}

      {data ? (
        <div className="space-y-6">
          <Card>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="font-mono text-xl font-extrabold text-navy-950">
                  {data.trackingNumber}
                </div>
                <div className="text-sm text-slate-600">
                  {data.origin} → {data.destination} · {data.carrier} · {data.mode}
                </div>
              </div>
              <StatusPill status={data.status} />
            </div>
            {brand?.company ? (
              <p className="mt-2 inline-flex items-center gap-1.5 rounded-full bg-risk-clear-bg px-2.5 py-1 text-[11px] font-bold text-risk-clear">
                <span aria-hidden="true">✓</span> Verified by {brand.company} — live tracking they operate
              </p>
            ) : null}
            {data.eta ? (
              <div className="mt-3 rounded-xl bg-slate-50 p-3 text-sm text-slate-600">
                {data.etaSource === 'carrier' ? 'Arriving' : 'Estimated arrival'}:{' '}
                <span className="font-mono font-semibold text-navy-950">
                  {new Date(data.eta).toLocaleString()}
                </span>
                <EtaBadge source={data.etaSource} />
              </div>
            ) : (
              <div className="mt-3 rounded-xl bg-slate-50 p-3 text-sm text-slate-600">
                {(() => {
                  const last = data.events[data.events.length - 1];
                  const when = data.lastUpdate ? relativeTime(data.lastUpdate) : null;
                  const where = last?.location ? ` at ${last.location}` : '';
                  // Ambiguity aversion: "No ETA yet — last ping 4h ago at
                  // Rotterdam" instead of "—". lastUpdate is the last carrier
                  // signal we actually hold; the tail checkpoint names it.
                  return (
                    <>
                      No ETA yet{when ? ` — last ping ${when}${where}` : ''} — we
                      show a date here as soon as the carrier publishes one.
                    </>
                  );
                })()}
              </div>
            )}
          </Card>
          <Card title={`Journey (${data.events.length} checkpoints)`}>
            {data.events.length > 0 ? (
              <Timeline events={data.events} audience="customer" />
            ) : (
              <Empty message="No checkpoints yet." />
            )}
          </Card>
          <SubscribeCard tracking={data.trackingNumber} accent={accent} />
        </div>
      ) : null}
    </div>
  );
}

function SubscribeCard({ tracking, accent }: { tracking: string; accent: string }) {
  const [channel, setChannel] = useState('email');
  const [recipient, setRecipient] = useState('');
  const [status, setStatus] = useState<'active' | 'pending' | null>(null);
  const [error, setError] = useState('');

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    try {
      const res = await fetch(
        `/api/v1/track/${encodeURIComponent(tracking)}/subscribe`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ channel, recipient: recipient.trim() }),
        },
      );
      const body = await res.json().catch(() => null);
      if (!res.ok) {
        throw new Error(body?.error?.message ?? 'Subscribe failed');
      }
      // Metered channels come back 'pending': the recipient must confirm before
      // anything is sent. Telling them "you are subscribed" here would be a
      // lie, and would put the platform in breach of WhatsApp's rules.
      setStatus(body?.status === 'pending' ? 'pending' : 'active');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Subscribe failed');
    }
  };

  if (status) {
    return (
      <Card>
        <p role="status" className="text-sm text-slate-600">
          {status === 'pending' ? (
            <>
              Almost there — check your {channel === 'email' ? 'inbox' : 'messages'} and
              confirm to start receiving updates for{' '}
              <span className="font-mono font-semibold">{tracking}</span>. Nothing
              is sent until you confirm; opt-out works from any message, anytime.
            </>
          ) : (
            <>
              You are subscribed — updates for{' '}
              <span className="font-mono font-semibold">{tracking}</span> are on the way.
            </>
          )}{' '}
          <span className="text-slate-500">
            {channel === 'email' ? 'Email confirmed' : 'Number confirmed'} · opt-out works from any message, anytime.
          </span>
        </p>
      </Card>
    );
  }

  return (
    <Card title="Notify me">
      <form onSubmit={submit} className="flex flex-wrap gap-2">
        <label htmlFor="channel" className="sr-only">
          Channel
        </label>
        <select
          id="channel"
          value={channel}
          onChange={(e) => setChannel(e.target.value)}
          className="rounded-xl border border-slate-300 px-3 py-2.5 text-sm focus:border-accent-500 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500"
        >
          <option value="email">Email</option>
          <option value="sms">SMS</option>
          <option value="whatsapp">WhatsApp</option>
        </select>
        <label htmlFor="recipient" className="sr-only">
          Email address or phone number
        </label>
        <input
          id="recipient"
          value={recipient}
          onChange={(e) => setRecipient(e.target.value)}
          placeholder={channel === 'email' ? 'you@example.com' : '+15551234567'}
          required
          className="min-w-52 flex-1 rounded-xl border border-slate-300 px-4 py-2.5 text-sm focus:border-accent-500 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-500"
        />
        <button
          type="submit"
          className="rounded-xl px-6 py-2.5 font-semibold text-white focus-visible:ring-2 focus-visible:ring-accent-500 focus-visible:ring-offset-2"
          style={{ background: accent }}
        >
          Subscribe
        </button>
      </form>
      {channel !== 'email' ? (
        <p className="mt-2 text-xs text-slate-600">
          We will send one confirmation message first. Nothing else is sent until you
          confirm — and you can opt out from any message.
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="mt-2 text-sm text-red-700">
          {error}
        </p>
      ) : null}
    </Card>
  );
}