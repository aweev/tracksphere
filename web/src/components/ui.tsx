import Link from 'next/link';
import type { ShipmentStatus } from '@/lib/api';

const labels: Record<ShipmentStatus, string> = {
  booked: 'Booked',
  in_transit: 'In transit',
  at_customs: 'At customs',
  out_for_delivery: 'Out for delivery',
  delivered: 'Delivered',
  exception: 'Exception',
  cancelled: 'Cancelled',
};

export function StatusPill({ status }: { status: ShipmentStatus }) {
  return (
    <span className={`status-pill status-${status}`}>
      <span className="h-1.5 w-1.5 rounded-full bg-current opacity-70" aria-hidden="true" />
      {labels[status]}
    </span>
  );
}

export function RiskBadge({
  tier,
  score,
}: {
  tier: 'critical' | 'at_risk' | 'watch' | 'clear';
  score?: number;
}) {
  return (
    <span className={`risk-badge risk-${tier}`} aria-label={`Risk ${tier.replace('_', ' ')} ${score ? `score ${score}` : ''}`}>
      {tier.replace('_', ' ')}
      {score !== undefined ? ` · ${score}` : ''}
    </span>
  );
}

export function Card({
  title,
  children,
  action,
}: {
  title?: string;
  children: React.ReactNode;
  action?: React.ReactNode;
}) {
  return (
    <section className="app-card rounded-3xl bg-white p-6 shadow-sm ring-1 ring-slate-200/70">
      {title ? (
        <header className="mb-4 flex items-center justify-between">
          <h2 className="text-sm font-bold uppercase tracking-wide text-slate-500">{title}</h2>
          {action}
        </header>
      ) : null}
      {children}
    </section>
  );
}

export function StatTile({
  label,
  value,
  tone = 'default',
  href,
}: {
  label: string;
  value: number;
  tone?: 'default' | 'accent' | 'danger' | 'success';
  href?: string;
}) {
  const tones = {
    default: 'text-navy-950',
    accent: 'text-accent-500',
    danger: 'text-red-600',
    success: 'text-emerald-600',
  };
  const body = (
    <>
      <div className={`text-3xl font-extrabold ${tones[tone]}`}>{value}</div>
      <div className="mt-1 text-xs font-semibold uppercase tracking-wide text-slate-500">
        {label}
      </div>
    </>
  );
  if (href) {
    return (
      <Link
        href={href}
        className="app-stat-tile block rounded-3xl bg-white p-5 shadow-sm ring-1 ring-slate-200/70 transition hover:ring-accent-500 focus-visible:ring-2 focus-visible:ring-accent-500"
      >
        {body}
      </Link>
    );
  }
  return (
    <div className="app-stat-tile rounded-3xl bg-white p-5 shadow-sm ring-1 ring-slate-200/70">
      {body}
    </div>
  );
}

export function Empty({ message }: { message: string }) {
  return (
    <div className="rounded-2xl border border-dashed border-slate-300 p-8 text-center text-sm text-slate-600" role="status">
      {message}
    </div>
  );
}

export function BackLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <Link href={href} className="text-sm font-semibold text-slate-500 hover:text-accent-500 focus-visible:ring-2 focus-visible:ring-accent-500 rounded">
      {children}
    </Link>
  );
}