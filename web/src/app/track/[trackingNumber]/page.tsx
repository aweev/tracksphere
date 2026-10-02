'use client';

import { useQuery } from '@tanstack/react-query';
import { use, useState, type FormEvent } from 'react';
import Link from 'next/link';
import { Card, Empty, StatusPill } from '@/components/ui';
import { Timeline } from '@/components/Timeline';
import { api, RequestError, type PublicTracking } from '@/lib/api';

export default function TrackByNumberPage({
  params,
}: {
  params: Promise<{ trackingNumber: string }>;
}) {
  const { trackingNumber } = use(params);
  return <PublicTrack initial={trackingNumber} />;
}

export function PublicTrackPage() {
  return <PublicTrack initial="" />;
}

/** Customer-facing tracker: no login, safe projection from the API only. */
function PublicTrack({ initial }: { initial: string }) {
  const [input, setInput] = useState(initial);
  const [tracking, setTracking] = useState(initial);

  const { data, error, isFetching } = useQuery({
    queryKey: ['track', tracking],
    queryFn: async () => (await api.get<PublicTracking>(`/api/v1/track/${tracking}`)).data,
    enabled: tracking.length > 0,
    retry: false,
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTracking(input.trim());
  };

  return (
    <div className="mx-auto max-w-3xl p-6">
      <header className="mb-8 pt-8 text-center">
        <Link href="/" className="text-2xl font-extrabold text-navy-950">
          Track<span className="text-accent-500">Sphere</span>
        </Link>
        <p className="mt-1 text-sm text-slate-500">Track your shipment in real time</p>
      </header>

      <form onSubmit={onSubmit} className="mb-8 flex gap-2">
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Enter tracking number (e.g. TS-8842-LAG)"
          className="flex-1 rounded-2xl border border-slate-300 px-5 py-3.5 font-mono focus:border-accent-500 focus:outline-none"
        />
        <button className="rounded-2xl bg-accent-500 px-6 py-3.5 font-semibold text-white hover:bg-accent-600">
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
                <div className="text-sm text-slate-500">
                  {data.origin} → {data.destination} · {data.carrier} · {data.mode}
                </div>
              </div>
              <StatusPill status={data.status} />
            </div>
            {data.eta ? (
              <div className="mt-3 rounded-xl bg-slate-50 p-3 text-sm text-slate-600">
                Estimated arrival:{' '}
                <span className="font-mono font-semibold text-navy-950">
                  {new Date(data.eta).toLocaleString()}
                </span>
              </div>
            ) : null}
          </Card>
          <Card title={`Journey (${data.events.length} checkpoints)`}>
            {data.events.length > 0 ? (
              <Timeline events={data.events} />
            ) : (
              <Empty message="No checkpoints yet." />
            )}
          </Card>
        </div>
      ) : null}
    </div>
  );
}
