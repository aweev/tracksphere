'use client';

import Link from 'next/link';
import { usePathname, useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { useLiveStream } from '@/lib/live';

const nav = [
  { href: '/', label: 'Dashboard', icon: '◧' },
  { href: '/shipments', label: 'Shipments', icon: '▣' },
  { href: '/track', label: 'Track (public)', icon: '◎' },
];

/** Control-tower shell: deep-navy sidebar + outlet, orange accent. */
export function Shell({ children }: { children: React.ReactNode }) {
  const { user, logout } = useAuth();
  const pathname = usePathname();
  const router = useRouter();
  useLiveStream(!!user);

  return (
    <div className="flex h-full">
      <aside className="flex w-64 shrink-0 flex-col bg-navy-950 text-slate-200">
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

        <nav className="mt-2 flex-1 space-y-1 px-3">
          {nav.map((item) => {
            const active = item.href === '/' ? pathname === '/' : pathname.startsWith(item.href);
            return (
              <Link
                key={item.href}
                href={item.href}
                className={`flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm font-medium transition ${
                  active
                    ? 'bg-navy-800 text-white'
                    : 'text-slate-400 hover:bg-navy-900 hover:text-white'
                }`}
              >
                <span className="text-base opacity-70">{item.icon}</span>
                {item.label}
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
    </div>
  );
}
