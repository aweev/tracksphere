'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import Link from 'next/link';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import { api, authApi, type NotificationItem } from '@/lib/api';
import { useAuth } from '@/lib/auth';

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
      <ChannelsCard />
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

/**
 * Metered-channel kill-switch UI (P2-4). SMS/WhatsApp default OFF tenant-wide
 * and every send path enforces it — these toggles are the switch. Admin-only
 * writes; members see read-only state.
 */
function ChannelsCard() {
  const { user } = useAuth();
  const qc = useQueryClient();
  const { data: prefs } = useQuery({
    queryKey: ['notify-prefs'],
    queryFn: async () => (await authApi.getNotifyPrefs()).data,
  });
  const patch = useMutation({
    mutationFn: (body: { smsEnabled?: boolean; whatsappEnabled?: boolean }) =>
      authApi.updateNotifyPrefs(body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['notify-prefs'] }),
  });
  const admin = user?.role === 'owner' || user?.role === 'admin';
  if (!prefs) return null;
  const row = (
    label: string,
    hint: string,
    on: boolean,
    set: (v: boolean) => void,
  ) => (
    <li className="flex flex-wrap items-center justify-between gap-2 py-3">
      <div>
        <div className="text-sm font-semibold text-navy-950">{label}</div>
        <div className="text-xs text-slate-500">{hint}</div>
      </div>
      <button
        type="button"
        disabled={!admin || patch.isPending}
        role="switch"
        aria-checked={on}
        aria-label={label}
        onClick={() => set(!on)}
        className={`rounded-full px-3 py-1.5 text-xs font-bold disabled:opacity-50 ${
          on ? 'bg-emerald-100 text-emerald-800' : 'bg-slate-100 text-slate-500'
        }`}
      >
        {on ? 'On' : 'Off'}
      </button>
    </li>
  );
  return (
    <Card title="Metered channels">
      <ul className="divide-y divide-slate-100">
        {row(
          'SMS',
          'Per-message cost via Twilio. Customers must still confirm each subscription.',
          prefs.smsEnabled,
          (v) => patch.mutate({ smsEnabled: v }),
        )}
        {row(
          'WhatsApp',
          'Per-message cost + Business policy exposure. Confirmation required.',
          prefs.whatsappEnabled,
          (v) => patch.mutate({ whatsappEnabled: v }),
        )}
      </ul>
      {!admin ? (
        <p className="mt-2 text-xs text-slate-500">Only admins can change channel switches.</p>
      ) : null}
    </Card>
  );
}
