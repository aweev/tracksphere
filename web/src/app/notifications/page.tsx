'use client';

import { useQuery } from '@tanstack/react-query';
import Link from 'next/link';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import { api, type NotificationItem } from '@/lib/api';

export default function NotificationsPage() {
  return (
    <RequireAuth>
      <NotificationsBody />
    </RequireAuth>
  );
}

function NotificationsBody() {
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['notifications'],
    queryFn: async () => (await api.get<NotificationItem[]>('/api/v1/notifications')).data,
  });
  const { data: providers } = useQuery({
    queryKey: ['notify-status'],
    queryFn: async () => (await api.get<{ providers: Record<string, boolean> }>('/api/v1/notify/status')).data,
  });
  const live = providers ? Object.entries(providers.providers ?? {}).filter(([, v]) => v).map(([k]) => k) : [];
  return (
    <div className="p-4 md:p-8">
      <header className="mb-6">
        <h1 className="text-2xl font-extrabold text-navy-950">Notifications</h1>
        <p className="text-sm text-slate-500">
          Every customer message sent{live.length > 0 ? ` · live providers: ${live.join(', ')}` : ''}
        </p>
      </header>
      {isLoading ? (
        <Card title="Loading…">
          <div className="animate-pulse space-y-3" aria-hidden="true">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-16 rounded-2xl bg-slate-100" />
            ))}
          </div>
        </Card>
      ) : error ? (
        <Card title="Unavailable">
          <p className="text-sm text-slate-600">Could not load notifications.</p>
          <button
            type="button"
            onClick={() => refetch()}
            className="mt-3 rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white"
          >
            Retry
          </button>
        </Card>
      ) : (data ?? []).length === 0 ? (
        <Empty message="No notifications yet — they appear here when carrier events fire." />
      ) : (
        <Card>
          <ul className="divide-y divide-slate-100">
            {(data ?? []).map((n) => (
              <li key={n.id} className="flex flex-wrap items-center justify-between gap-2 py-3">
                <div>
                  <div className="text-sm font-semibold text-navy-950">{n.subject}</div>
                  <div className="font-mono text-xs text-slate-500">
                    {n.channel} → {n.recipient} · {n.status} · {new Date(n.createdAt).toLocaleString()}
                  </div>
                </div>
                {n.shipmentId ? (
                  <Link
                    href={`/shipments/${n.shipmentId}`}
                    className="text-xs font-semibold text-accent-500 hover:underline"
                  >
                    Open shipment →
                  </Link>
                ) : null}
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
