'use client';

import Link from 'next/link';
import { usePathname, useRouter } from 'next/navigation';
import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { useLiveStream } from '@/lib/live';
import { api, type Alert, type Shipment } from '@/lib/api';

const nav = [
  { href: '/', label: 'Dashboard', icon: 'dashboard' },
  { href: '/shipments', label: 'Shipments', icon: 'shipments' },
  { href: '/exceptions', label: 'Exceptions', icon: 'exceptions', badgeKey: 'exceptions' as const },
  { href: '/analytics', label: 'Analytics', icon: 'analytics' },
  { href: '/notifications', label: 'Notifications', icon: 'notifications' },
  { href: '/integrations', label: 'Integrations', icon: 'integrations' },
  { href: '/settings', label: 'Settings', icon: 'settings' },
  { href: '/track', label: 'Public tracking', icon: 'tracking' },
];

type NavIconName = (typeof nav)[number]['icon'];

const navIconPaths: Record<NavIconName, React.ReactNode> = {
  dashboard: <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>,
  shipments: <><path d="m3 7 9-4 9 4-9 4-9-4Z" /><path d="M3 7v10l9 4 9-4V7M12 11v10" /><path d="m7.5 5 9 4" /></>,
  exceptions: <><path d="m12 3 9 17H3L12 3Z" /><path d="M12 9v4m0 3h.01" /></>,
  analytics: <><path d="M4 19V5m0 14h17" /><path d="m7 15 4-4 3 2 6-7" /><path d="M17 6h3v3" /></>,
  notifications: <><path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9" /><path d="M10 21h4" /></>,
  integrations: <><path d="M8 7V4m8 3V4M7 8h10a3 3 0 0 1 3 3v3a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3v-3a3 3 0 0 1 3-3Z" /><path d="M8 12h.01M16 12h.01M12 17v3" /></>,
  settings: <><circle cx="12" cy="12" r="3" /><path d="m19.4 15 .1.1 1.4 1.1-1.4 2.4-1.7-.7a8 8 0 0 1-1.7 1l-.3 1.8h-2.8l-.3-1.8a8 8 0 0 1-1.7-1l-1.7.7-1.4-2.4 1.4-1.1a7 7 0 0 1 0-2l-1.4-1.1 1.4-2.4 1.7.7a8 8 0 0 1 1.7-1l.3-1.8h2.8l.3 1.8a8 8 0 0 1 1.7 1l1.7-.7 1.4 2.4-1.4 1.1a7 7 0 0 1 0 2Z" transform="translate(-1 -1)" /></>,
  tracking: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18m0-18a14 14 0 0 0 0 18" /></>,
};

function NavIcon({ name }: { name: NavIconName }) {
  return (
    <svg
      className="app-nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {navIconPaths[name]}
    </svg>
  );
}

const pageTitles: Record<string, string> = {
  '/': 'Operations overview',
  '/shipments': 'Shipments',
  '/exceptions': 'Exception queue',
  '/analytics': 'Analytics',
  '/notifications': 'Notifications',
  '/integrations': 'Integrations',
  '/settings': 'Settings',
  '/track': 'Public tracking',
};

/** Shared operations-console navigation and live workspace chrome. */
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
  const pageTitle =
    pageTitles[pathname] ??
    (pathname.startsWith('/shipments/') ? 'Shipment details' : 'TrackSphere');

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
    <div className="app-shell flex h-full">
      <aside className="app-sidebar flex w-64 shrink-0 flex-col text-slate-200 max-md:hidden">
        <Link href="/" className="app-brand flex items-center gap-3 px-6 py-6">
          <div className="app-brand-mark flex h-10 w-10 items-center justify-center rounded-xl font-bold text-white">
            <span aria-hidden="true">T</span>
          </div>
          <div>
            <div className="text-lg font-extrabold tracking-tight text-white">
              Track<span className="text-accent-500">Sphere</span>
            </div>
            <div className="font-mono text-[9px] uppercase tracking-[0.2em] text-slate-400">
              Logistics intelligence
            </div>
          </div>
        </Link>

        <div className="px-4">
          <button
            type="button"
            onClick={() => setPalette(true)}
            className="app-search-trigger w-full rounded-xl border px-3 py-2.5 text-left text-xs text-slate-300 transition"
          >
            <span aria-hidden="true" className="mr-2">⌕</span>
            Search anything…
            <kbd className="float-right font-mono">⌘ K</kbd>
          </button>
        </div>

        <div className="app-nav-label px-6 pt-8 pb-2">Workspace</div>
        <nav className="flex-1 space-y-1 px-3" aria-label="Primary">
          {nav.map((item) => {
            const active = item.href === '/' ? pathname === '/' : pathname.startsWith(item.href);
            return (
              <Link
                key={item.href}
                href={item.href}
                aria-current={active ? 'page' : undefined}
                className={`app-nav-link flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm font-medium transition ${
                  active
                    ? 'is-active text-white'
                    : 'text-slate-300 hover:text-white'
                }`}
              >
                <NavIcon name={item.icon} />
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

        <div className="app-account mx-3 mb-3 rounded-2xl p-3">
          <div className="flex min-w-0 items-center gap-3">
            <div className="app-avatar flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-xs font-bold">
              {(user?.name || user?.email || 'T').slice(0, 1).toUpperCase()}
            </div>
            <div className="min-w-0">
              <div className="truncate text-sm font-semibold text-white">{user?.name || user?.email}</div>
              <div className="truncate text-[11px] text-slate-400">{user?.email}</div>
            </div>
          </div>
          <button
            onClick={async () => {
              await logout();
              router.push('/login');
            }}
            className="app-signout mt-3 w-full rounded-lg border px-3 py-2 text-xs font-semibold text-slate-300 transition"
          >
            Sign out
          </button>
        </div>
      </aside>

      <main className="app-main min-w-0 flex-1 overflow-y-auto">
        <header className="app-topbar flex items-center justify-between gap-4 px-5 md:px-8">
          <div className="flex min-w-0 items-center gap-3">
            <div className="app-mobile-mark hidden h-9 w-9 items-center justify-center rounded-xl font-bold text-white md:hidden">T</div>
            <div className="min-w-0">
              <div className="app-breadcrumb text-[11px] font-medium uppercase tracking-[0.14em]">TrackSphere <span aria-hidden="true">/</span></div>
              <div className="truncate text-sm font-bold">{pageTitle}</div>
            </div>
          </div>
          <div className="flex items-center gap-3">
            <div className="app-live-indicator hidden items-center gap-2 text-xs font-medium sm:flex">
              <span className="app-live-dot" aria-hidden="true" />
              Live operations
            </div>
            <button
              type="button"
              onClick={() => setPalette(true)}
              className="app-top-search rounded-xl px-3 py-2 text-xs font-semibold transition"
              aria-label="Search shipments"
            >
              <span aria-hidden="true">⌕</span>
              <span className="ml-2 hidden sm:inline">Search</span>
              <kbd className="ml-3 hidden font-mono sm:inline">⌘K</kbd>
            </button>
            <div className="app-top-avatar flex h-9 w-9 items-center justify-center rounded-full text-xs font-bold" aria-label={user?.name || user?.email || 'Account'}>
              {(user?.name || user?.email || 'T').slice(0, 1).toUpperCase()}
            </div>
          </div>
        </header>
        <nav className="app-mobile-nav hidden" aria-label="Mobile primary">
          {nav.map((item) => {
            const active = item.href === '/' ? pathname === '/' : pathname.startsWith(item.href);
            return (
              <Link key={item.href} href={item.href} aria-current={active ? 'page' : undefined}>
                <NavIcon name={item.icon} />
                <span className="app-mobile-nav-label">{item.label}</span>
              </Link>
            );
          })}
        </nav>
        {children}
      </main>
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
      className="app-command-overlay fixed inset-0 z-50 flex items-start justify-center p-4 pt-24"
      onClick={close}
      role="dialog"
      aria-modal="true"
      aria-label="Global search"
    >
      <div className="app-command-dialog w-full max-w-lg rounded-2xl p-4 shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search tracking # or reference…"
          aria-label="Global shipment search"
          className="app-command-input w-full rounded-xl border px-4 py-3 text-sm focus:border-accent-500 focus:outline-none"
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
