'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import Link from 'next/link';
import { useMemo, useState } from 'react';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import {
  api,
  authApi,
  RequestError,
  type Alert,
  type AlertRootCause,
  type TeamMember,
} from '@/lib/api';

type SeverityFilter = 'all' | Alert['severity'];

/**
 * Root-cause taxonomy. Deliberately closed: free-text causes cannot be
 * aggregated into carrier scorecards, and the scorecards are the moat.
 */
const ROOT_CAUSES: { value: AlertRootCause; label: string }[] = [
  { value: 'documentation', label: 'Documentation' },
  { value: 'customs_duty', label: 'Customs / duty' },
  { value: 'customs_processing', label: 'Customs processing' },
  { value: 'carrier_delay', label: 'Carrier delay' },
  { value: 'missed_connection', label: 'Missed connection' },
  { value: 'blank_sailing', label: 'Blank sailing' },
  { value: 'port_congestion', label: 'Port congestion' },
  { value: 'capacity', label: 'Capacity' },
  { value: 'weather', label: 'Weather' },
  { value: 'carrier_error', label: 'Carrier error' },
  { value: 'shipper_delay', label: 'Shipper delay' },
  { value: 'other', label: 'Other' },
];

const SEVERITY_STYLES: Record<
  Alert['severity'],
  { chip: string; border: string; icon: string; label: string }
> = {
  // Severity is encoded by colour AND icon AND text, never colour alone: a
  // red border on its own is a WCAG 1.4.1 failure and invisible to anyone
  // who cannot distinguish red from amber.
  critical: {
    chip: 'bg-red-100 text-red-700',
    border: 'border-l-red-500',
    icon: '▲',
    label: 'Critical',
  },
  warning: {
    chip: 'bg-amber-100 text-amber-800',
    border: 'border-l-amber-500',
    icon: '◆',
    label: 'Warning',
  },
  info: {
    chip: 'bg-blue-100 text-blue-700',
    border: 'border-l-blue-500',
    icon: '●',
    label: 'Info',
  },
};

const RISK_LABELS: Record<string, string> = {
  clear: 'On track',
  watch: 'Watch',
  at_risk: 'At risk',
  critical: 'Critical',
};

function ageLabel(iso: string): string {
  const mins = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 60000));
  if (mins < 60) return `${mins}m old`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `${hours}h old`;
  return `${Math.round(hours / 24)}d old`;
}

/** SLA countdown against the exception's own due date, not the shipment ETA. */
function slaLabel(a: Alert): { text: string; breached: boolean } | null {
  if (!a.dueAt) return null;
  if (a.status === 'resolved') return null;
  const diffMs = new Date(a.dueAt).getTime() - Date.now();
  const hours = Math.round(diffMs / 3600000);
  if (hours < 0) {
    return { text: `SLA breached ${Math.abs(hours)}h ago`, breached: true };
  }
  if (hours <= 24) return { text: `SLA due in ${hours}h`, breached: false };
  return null;
}

export default function ExceptionsPage() {
  return (
    <RequireAuth>
      <ExceptionsBody />
    </RequireAuth>
  );
}

function ExceptionsBody() {
  const queryClient = useQueryClient();
  const [severity, setSeverity] = useState<SeverityFilter>('all');
  const [includeSnoozed, setIncludeSnoozed] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);

  const alertsKey = ['alerts', 'open', includeSnoozed];
  const { data: alerts, isLoading, error, refetch } = useQuery({
    queryKey: alertsKey,
    queryFn: async () =>
      (
        await api.get<Alert[]>(
          `/api/v1/alerts?status=open${includeSnoozed ? '&include=snoozed' : ''}`,
        )
      ).data,
  });

  // Team list powers assignment. A 403 here is expected for members, and must
  // not break the page — they can still triage, just not reassign.
  const { data: team } = useQuery({
    queryKey: ['team'],
    queryFn: () => authApi.listTeam(),
    retry: false,
  });

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['alerts'] });
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  };

  const resolve = useMutation({
    mutationFn: ({ id, rootCause, note }: { id: string; rootCause?: AlertRootCause; note?: string }) =>
      authApi.resolveAlert(id, { rootCause, note }),
    onSuccess: refresh,
  });

  const assign = useMutation({
    mutationFn: ({
      id,
      userId,
      snoozeMinutes,
      rootCause,
    }: {
      id: string;
      userId?: string;
      snoozeMinutes?: number;
      rootCause?: AlertRootCause;
    }) => authApi.assignAlert(id, { userId, snoozeMinutes, rootCause }),
    onSuccess: refresh,
  });

  const filtered = useMemo(
    () => (alerts ?? []).filter((a) => severity === 'all' || a.severity === severity),
    [alerts, severity],
  );

  const stats = useMemo(() => {
    const list = alerts ?? [];
    return {
      total: list.length,
      critical: list.filter((a) => a.severity === 'critical').length,
      breached: list.filter((a) => slaLabel(a)?.breached).length,
      unowned: list.filter((a) => !a.assignedTo).length,
      untold: list.filter((a) => !a.customerNotified).length,
    };
  }, [alerts]);

  return (
    <div className="p-4 md:p-8">
      <header className="mb-6 flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-extrabold text-navy-950">Exceptions</h1>
          <p className="text-sm text-slate-500">
            Worst first. <span className="font-semibold">{stats.unowned}</span> unowned ·{' '}
            <span className="font-semibold">{stats.breached}</span> past SLA ·{' '}
            <span className="font-semibold">{stats.untold}</span> customer not told
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex gap-2" role="group" aria-label="Severity filter">
            {(['all', 'critical', 'warning', 'info'] as SeverityFilter[]).map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => setSeverity(s)}
                aria-pressed={severity === s}
                className={`rounded-xl px-3 py-2 text-xs font-semibold focus-visible:ring-2 focus-visible:ring-accent-500 ${
                  severity === s
                    ? 'bg-navy-950 text-white'
                    : 'border border-slate-200 text-slate-600'
                }`}
              >
                {s === 'all' ? 'All' : SEVERITY_STYLES[s].label}
              </button>
            ))}
          </div>
          <label className="flex items-center gap-2 text-xs font-semibold text-slate-600">
            <input
              type="checkbox"
              checked={includeSnoozed}
              onChange={(e) => setIncludeSnoozed(e.target.checked)}
              className="h-4 w-4 rounded border-slate-300"
            />
            Show snoozed
          </label>
        </div>
      </header>

      {isLoading ? (
        <Card title="Loading exceptions…">
          <div className="animate-pulse space-y-3" aria-hidden="true">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-20 rounded-2xl bg-slate-100" />
            ))}
          </div>
        </Card>
      ) : error ? (
        <Card title="Exceptions unavailable">
          <p className="text-sm text-slate-600">
            {error instanceof RequestError ? error.message : 'Could not load exceptions.'}
          </p>
          <button
            type="button"
            onClick={() => refetch()}
            className="mt-3 rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white"
          >
            Retry
          </button>
        </Card>
      ) : filtered.length === 0 ? (
        // A healthy control tower should be able to look quiet. Restraint is a
        // feature: an always-anxious dashboard trains a team to ignore it.
        <Empty message="All clear — no open exceptions." />
      ) : (
        <ul className="space-y-3">
          {filtered.map((a) => {
            const style = SEVERITY_STYLES[a.severity];
            const sla = slaLabel(a);
            const open = expanded === a.id;
            return (
              <li
                key={a.id}
                className={`rounded-2xl border-l-4 bg-white p-4 shadow-sm ${style.border}`}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span
                        className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-bold uppercase tracking-wide ${style.chip}`}
                      >
                        <span aria-hidden="true">{style.icon}</span>
                        {style.label}
                      </span>
                      {a.assignedToName ? (
                        <span className="rounded-full bg-slate-100 px-2 py-0.5 text-[11px] font-semibold text-slate-700">
                          {a.assignedToName}
                        </span>
                      ) : (
                        <span className="rounded-full bg-orange-100 px-2 py-0.5 text-[11px] font-semibold text-orange-800">
                          Unowned
                        </span>
                      )}
                      {a.escalatedAt ? (
                        <span className="rounded-full bg-red-600 px-2 py-0.5 text-[11px] font-bold text-white">
                          Escalated
                        </span>
                      ) : null}
                      {a.snoozedUntil && new Date(a.snoozedUntil) > new Date() ? (
                        <span className="rounded-full bg-slate-200 px-2 py-0.5 text-[11px] font-semibold text-slate-700">
                          Snoozed
                        </span>
                      ) : null}
                    </div>

                    <div className="mt-1 text-sm font-bold text-navy-950">{a.title}</div>
                    <div className="mt-0.5 text-xs text-slate-600">{a.message}</div>

                    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] text-slate-400">
                      {a.trackingNumber ? (
                        <Link
                          href={`/shipments/${a.shipmentId}`}
                          className="font-semibold text-navy-950 hover:underline"
                        >
                          {a.trackingNumber}
                        </Link>
                      ) : null}
                      <span>{a.kind}</span>
                      <span>{ageLabel(a.detectedAt ?? a.createdAt)}</span>
                      {a.riskTier && a.riskTier !== 'clear' ? (
                        <span className="font-sans font-bold text-slate-600">
                          {RISK_LABELS[a.riskTier]} ({a.riskScore})
                        </span>
                      ) : null}
                      {a.staleHours && a.staleHours > 24 ? (
                        <span className="font-sans font-bold text-orange-700">
                          No scan {Math.round(a.staleHours)}h
                        </span>
                      ) : null}
                      {a.valueAtRisk ? (
                        <span className="font-sans font-bold text-slate-600">
                          ${Math.round(a.valueAtRisk).toLocaleString()} at risk
                        </span>
                      ) : null}
                      {a.customerNotified ? (
                        <span className="font-sans font-semibold text-emerald-700">
                          Customer told
                        </span>
                      ) : (
                        <span className="font-sans font-semibold text-slate-500">
                          Customer not told
                        </span>
                      )}
                      {sla ? (
                        <span
                          className={`font-sans font-bold ${sla.breached ? 'text-red-600' : 'text-amber-700'}`}
                        >
                          {sla.text}
                        </span>
                      ) : null}
                    </div>

                    {a.rootCause ? (
                      <div className="mt-1 text-[11px] text-slate-500">
                        Cause:{' '}
                        <span className="font-semibold">
                          {ROOT_CAUSES.find((r) => r.value === a.rootCause)?.label ??
                            a.rootCause}
                        </span>
                        {a.note ? ` · ${a.note}` : ''}
                      </div>
                    ) : null}
                  </div>

                  <div className="flex shrink-0 flex-wrap gap-2">
                    <button
                      type="button"
                      onClick={() => setExpanded(open ? null : a.id)}
                      aria-expanded={open}
                      className="rounded-xl border border-slate-200 px-3 py-2 text-xs font-semibold text-navy-950"
                    >
                      {open ? 'Close' : 'Triage'}
                    </button>
                    <Link
                      href={`/shipments/${a.shipmentId}`}
                      className="rounded-xl border border-slate-200 px-3 py-2 text-xs font-semibold text-navy-950"
                    >
                      Open →
                    </Link>
                  </div>
                </div>

                {open ? (
                  <TriagePanel
                    alert={a}
                    team={team}
                    onAssign={(patch) => assign.mutate({ id: a.id, ...patch })}
                    onResolve={(rootCause, note) => resolve.mutate({ id: a.id, rootCause, note })}
                    busy={
                      assign.isPending || resolve.isPending
                    }
                    failed={assign.isError || resolve.isError}
                  />
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

/**
 * The triage panel. An exception card has to answer seven questions or it is
 * not actionable: what happened, which shipment, why it matters, the likely
 * cause, what to do, who owns it and its clock, and whether the customer knows.
 */
function TriagePanel({
  alert,
  team,
  onAssign,
  onResolve,
  busy,
  failed,
}: {
  alert: Alert;
  team?: TeamMember[];
  onAssign: (patch: {
    userId?: string;
    snoozeMinutes?: number;
    rootCause?: AlertRootCause;
  }) => void;
  onResolve: (rootCause: AlertRootCause, note: string) => void;
  busy: boolean;
  failed: boolean;
}) {
  const [cause, setCause] = useState<AlertRootCause>(alert.rootCause ?? 'other');
  const [note, setNote] = useState(alert.note ?? '');
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' } | null>(null);

  const customerUpdate = `Update on ${alert.trackingNumber ?? 'your shipment'}: ${alert.title} — ${alert.message}`;

  const sendCustomerUpdate = async (tracking: string, title: string, message: string) => {
    try {
      const res = await fetch(`/api/v1/shipments/${alert.shipmentId}/notify`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tracking, title, message, customerUpdate }),
      });
      if (res.ok) {
        setToast({ message: 'Customer update sent', type: 'success' });
      } else {
        setToast({ message: 'Failed to send customer update', type: 'error' });
      }
    } catch {
      setToast({ message: 'Failed to send customer update', type: 'error' });
    }
  };

  const emailCarrier = async (tracking: string, title: string, message: string, note: string) => {
    try {
      const res = await fetch(`/api/v1/shipments/${alert.shipmentId}/email-carrier`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tracking, title, message, note }),
      });
      if (res.ok) {
        setToast({ message: 'Carrier email queued', type: 'success' });
      } else {
        setToast({ message: 'Failed to queue carrier email', type: 'error' });
      }
    } catch {
      setToast({ message: 'Failed to queue carrier email', type: 'error' });
    }
  };

  return (
    <div className="mt-4 space-y-3 rounded-xl border border-slate-200 bg-slate-50 p-3">
      {toast && (
        <div className={`rounded-lg px-3 py-2 text-xs font-semibold ${toast.type === 'success' ? 'bg-emerald-100 text-emerald-800' : 'bg-red-100 text-red-800'}`}>
          {toast.message}
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block text-xs font-semibold text-slate-700">
          Owner
          <select
            value={alert.assignedTo ?? ''}
            disabled={busy || !team?.length}
            onChange={(e) =>
              onAssign({ userId: e.target.value || undefined })
            }
            className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 text-sm font-normal"
          >
            <option value="">Unassigned</option>
            {team?.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name || m.email}
              </option>
            ))}
          </select>
        </label>
        <label className="block text-xs font-semibold text-slate-700">
          Snooze
          <select
            disabled={busy}
            onChange={(e) => {
              const v = Number(e.target.value);
              if (v > 0) onAssign({ snoozeMinutes: v });
            }}
            defaultValue="0"
            className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 text-sm font-normal"
          >
            <option value="0">Not snoozed</option>
            <option value="60">1 hour</option>
            <option value="240">4 hours</option>
            <option value="1440">Tomorrow</option>
          </select>
        </label>
      </div>

      <label className="block text-xs font-semibold text-slate-700">
        Root cause
        <select
          value={cause}
          disabled={busy}
          onChange={(e) => setCause(e.target.value as AlertRootCause)}
          className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 text-sm font-normal"
        >
          {ROOT_CAUSES.map((r) => (
            <option key={r.value} value={r.value}>
              {r.label}
            </option>
          ))}
        </select>
      </label>

      <label className="block text-xs font-semibold text-slate-700">
        Note for the team
        <input
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="What did you find? What is still needed?"
          className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 text-sm font-normal"
        />
      </label>

      <div className="flex flex-wrap items-center gap-2 pt-1">
        <button
          type="button"
          disabled={busy}
          onClick={() => onResolve(cause, note.trim())}
          className="rounded-xl bg-navy-950 px-4 py-2 text-xs font-semibold text-white disabled:opacity-50"
        >
          Resolve with cause
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(customerUpdate);
            } catch {
              /* clipboard unavailable */
            }
          }}
          className="rounded-xl border border-slate-300 bg-white px-4 py-2 text-xs font-semibold text-navy-950"
        >
          Copy customer update
        </button>
        {!alert.customerNotified ? (
          <button
            type="button"
            disabled={busy}
            onClick={() => sendCustomerUpdate(alert.trackingNumber ?? '', alert.title, alert.message)}
            className="rounded-xl bg-emerald-600 px-4 py-2 text-xs font-semibold text-white disabled:opacity-50"
          >
            Send customer update
          </button>
        ) : null}
        <button
          type="button"
          disabled={busy}
          onClick={() => emailCarrier(alert.trackingNumber ?? '', alert.title, alert.message, note.trim())}
          className="rounded-xl bg-amber-600 px-4 py-2 text-xs font-semibold text-white disabled:opacity-50"
        >
          Email carrier
        </button>
        {!alert.customerNotified ? (
          <span className="text-xs text-slate-500">
            Customer has not been told — send the update before closing.
          </span>
        ) : null}
      </div>
      {failed ? (
        <p role="alert" className="text-xs text-red-600">
          Action failed — try again.
        </p>
      ) : null}
    </div>
  );
}