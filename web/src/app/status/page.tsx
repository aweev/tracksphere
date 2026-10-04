'use client';

import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import Link from 'next/link';

interface Status {
  status: string;
  region: string;
  version: string;
  uptimeSec: number;
  queue: { pending: number; dead: number };
}

export default function StatusPage() {
  const [data, setData] = useState<Status | null>(null);
  const { refetch } = useQuery({
    queryKey: ['statusz'],
    queryFn: async () => {
      const res = await fetch('/api/v1/statusz');
      const body = await res.json();
      setData(body.data);
      return body.data;
    },
    refetchInterval: 30000,
  });
  useEffect(() => {
    void refetch();
  }, [refetch]);
  const ok = data?.status === 'operational';
  return (
    <div className="mx-auto max-w-2xl p-6">
      <header className="mb-8 pt-8 text-center">
        <Link href="/track" className="text-2xl font-extrabold text-navy-950">
          Track<span className="text-accent-500">Sphere</span>
        </Link>
        <p className="mt-1 text-sm text-slate-500">System status</p>
      </header>
      <div
        role="status"
        className={`rounded-3xl p-6 text-white ${ok ? 'bg-emerald-600' : 'bg-amber-600'}`}
      >
        <div className="text-xl font-extrabold">
          {data ? (ok ? 'All systems operational' : `Status: ${data.status}`) : 'Checking…'}
        </div>
        {data ? (
          <dl className="mt-3 grid grid-cols-2 gap-2 text-sm opacity-90">
            <div><dt className="uppercase text-xs">Region</dt><dd className="font-mono">{data.region}</dd></div>
            <div><dt className="uppercase text-xs">Version</dt><dd className="font-mono">{data.version}</dd></div>
            <div><dt className="uppercase text-xs">Uptime</dt><dd className="font-mono">{Math.round(data.uptimeSec / 60)}m</dd></div>
            <div><dt className="uppercase text-xs">Queue</dt><dd className="font-mono">{data.queue.pending} pending · {data.queue.dead} dead</dd></div>
          </dl>
        ) : null}
      </div>
      <p className="mt-4 text-center text-xs text-slate-400">
        Refreshes every 30s · queue depth and dead letters included
      </p>
    </div>
  );
}
