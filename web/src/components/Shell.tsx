'use client';

import Link from 'next/link';
import { usePathname, useRouter } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { useLiveStream } from '@/lib/live';
import { api, type Alert, type Shipment } from '@/lib/api';

const nav = [
  { href: '/', label: 'Dashboard', icon: '◧' },
  { href: '/shipments', label: 'Shipments', icon: '▣' },
  { href: '/exceptions', label: 'Exceptions', icon: '⚠', badgeKey: 'exceptions' as const },
  { href: '/analytics', label: 'Analytics', icon: '◫' },
  { href: '/notifications', label: 'Notifications', icon: '✉' },
  { href: '/integrations', label: 'Integrations', icon: '⬣' },
  { href: '/settings', label: 'Settings', icon: '⚙' },
  { href: '/track', label: 'Track (public)', icon: '◎' },
];

/** Control-tower shell: deep-navy sidebar + outlet, orange accent. */
export function Shell({ children }: { children: React.ReactNode }) {
  const { user, logout } = useAuth();
  const pathname = usePathname();
  const router = useRouter();
  useLiveStream(!!user);
  const [palette, setPalette] = useState(false);
  const { data: openAlerts } = useQuery({
    queryKey: ['alerts', 'open'],
    queryFn: async () =>
      (
        await api.get<{ alerts: Alert[]; nextCursor?: string }>(
          '/api/v1/alerts?status=open&limit=50',
        )
      ).data,
    enabled: !!user,
    refetchInterval: 30000,
  });
  // The queue deliberately returns no total (counting the whole open set on
  // every 30s badge poll is the query pagination exists to avoid), so the
  // badge shows the first page size with a "+" when more pages exist.
  const exceptionCount = openAlerts?.alerts.length ?? 0;
  const exceptionOverflow = openAlerts?.nextCursor ? '+' : '';

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setPalette((v) => !v);
      }
      if (e.key === 'Escape') setPalette(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <div className="flex h-full">
      <aside className="flex w-64 shrink-0 flex-col bg-navy-950 text-slate-200 max-md:hidden">
        <div className="flex items-center gap-3 px-6 py-6">
          <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-accent-500 font-bold text-white">
            T
          </div>
          <div>
            <div className="text-lg font-extrabold text-white">
              Track<span className="text-accent-500">Sphere</span>
            </div>
            <div className="font-mono text-[10px] uppercase tracking-widest text-slate-500">
              Control Tower
            </div>
          </div>
        </div>

        <div className="px-3">
          <button
            type="button"
            onClick={() => setPalette(true)}
            className="w-full rounded-xl border border-navy-800 px-3 py-2 text-left text-xs text-slate-400 hover:border-accent-500"
          >
            Search… <span className="float-right font-mono">⌘K</span>
          </button>
        </div>

        <nav className="mt-2 flex-1 space-y-1 px-3" aria-label="Primary">
          {nav.map((item) => {
            const active = item.href === '/' ? pathname === '/' : pathname.startsWith(item.href);
            return (
              <Link
                key={item.href}
                href={item.href}
                aria-current={active ? 'page' : undefined}
                className={`flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm font-medium transition ${
                  active
                    ? 'bg-navy-800 text-white'
                    : 'text-slate-400 hover:bg-navy-900 hover:text-white'
                }`}
              >
                <span className="text-base opacity-70" aria-hidden="true">{item.icon}</span>
                <span className="flex-1">{item.label}</span>
                {'badgeKey' in item && item.badgeKey === 'exceptions' && exceptionCount > 0 ? (
                  <span
                    aria-label={`${exceptionCount}${exceptionOverflow} open exceptions`}
                    className="rounded-full bg-red-500 px-2 py-0.5 text-[11px] font-bold text-white"
                  >
                    {exceptionCount}
                    {exceptionOverflow}
                  </span>
                ) : null}
              </Link>
            );
          })}
        </nav>

        <div className="border-t border-navy-800 p-4">
          <div className="truncate text-sm font-semibold text-white">{user?.name || user?.email}</div>
          <div className="truncate text-xs text-slate-500">{user?.email}</div>
          <button
            onClick={async () => {
              await logout();
              router.push('/login');
            }}
            className="mt-3 w-full rounded-lg border border-navy-800 px-3 py-1.5 text-xs font-semibold text-slate-300 hover:border-accent-500 hover:text-accent-500"
          >
            Sign out
          </button>
        </div>
      </aside>

      <main className="flex-1 overflow-y-auto">{children}</main>
      {palette ? <CommandPalette close={() => setPalette(false)} /> : null}
    </div>
  );
}

function CommandPalette({ close }: { close: () => void }) {
  const [q, setQ] = useState('');
  const [debounced, setDebounced] = useState('');
  const router = useRouter();
  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);
  const { data } = useQuery({
    queryKey: ['cmdk', debounced],
    queryFn: async () =>
      (await api.get<Shipment[]>(`/api/v1/shipments?search=${encodeURIComponent(debounced)}&limit=8`)).data,
    enabled: debounced.length > 1,
  });
  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 p-4 pt-24"
      onClick={close}
      role="dialog"
      aria-label="Global search"
    >
      <div className="w-full max-w-lg rounded-2xl bg-white p-4 shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search tracking # or reference…"
          aria-label="Global shipment search"
          className="w-full rounded-xl border border-slate-300 px-4 py-3 text-sm focus:border-accent-500 focus:outline-none"
        />
        <ul className="mt-2 max-h-72 overflow-y-auto">
          {(data ?? []).map((s) => (
            <li key={s.id}>
              <button
                type="button"
                onClick={() => {
                  close();
                  router.push(`/shipments/${s.id}`);
                }}
                className="flex w-full items-center justify-between rounded-xl px-3 py-2.5 text-left hover:bg-slate-50"
              >
                <span>
                  <span className="block font-mono text-sm font-semibold text-navy-950">
                    {s.trackingNumber}
                  </span>
                  <span className="block text-xs text-slate-500">
                    {s.origin} → {s.destination} · {s.carrier}
                  </span>
                </span>
                <span className="text-xs capitalize text-slate-400">{s.status.replace('_', ' ')}</span>
              </button>
            </li>
          ))}
          {debounced.length > 1 && (data ?? []).length === 0 ? (
            <li className="px-3 py-4 text-sm text-slate-500">No matches.</li>
          ) : null}
        </ul>
      </div>
    </div>
  );
}
