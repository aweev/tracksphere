import type { ShipmentEvent } from '@/lib/api';

export function Timeline({ events }: { events: ShipmentEvent[] }) {
  return (
    <ol className="relative ml-3 space-y-6 border-l-2 border-slate-200 pl-6">
      {[...events].reverse().map((e) => (
        <li key={e.id} className="relative">
          <span
            className={`absolute -left-[31px] top-1 h-3 w-3 rounded-full ring-4 ring-white ${
              e.code.includes('HOLD') || e.code.includes('EXCEPTION')
                ? 'bg-red-500'
                : e.code === 'DELIVERED'
                  ? 'bg-emerald-500'
                  : 'bg-accent-500'
            }`}
          />
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <span className="font-mono text-sm font-bold text-navy-950">{e.code}</span>
            <span className="font-mono text-xs text-slate-400">
              {new Date(e.occurredAt).toLocaleString()}
            </span>
          </div>
          <div className="text-sm text-slate-600">{e.description}</div>
          {e.location ? (
            <div className="text-xs font-semibold uppercase tracking-wide text-slate-400">
              {e.location}
            </div>
          ) : null}
        </li>
      ))}
    </ol>
  );
}

export function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-slate-400">{label}</dt>
      <dd className="text-right font-semibold capitalize text-navy-950">{value}</dd>
    </div>
  );
}
