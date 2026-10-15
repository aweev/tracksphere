'use client';

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import Link from 'next/link';
import { useMemo, useState } from 'react';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import { RiskTooltip } from '@/components/RiskTooltip';
import {
  api,
  authApi,
  RequestError,
  type Alert,
  type AlertPage,
  type AlertRootCause,
  type TeamMember,
} from '@/lib/api';
import { relativeTime } from '@/lib/format';

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
  // who cannot distinguish red from amber. Colours come from the semantic
  // @theme risk tokens (globals.css) — never hardcoded palettes — so risk
  // means the same everywhere and dark mode follows automatically.
  critical: {
    chip: 'bg-risk-critical-bg text-risk-critical',
    border: 'border-l-risk-critical',
    icon: '▲',
    label: 'Critical',
  },
  warning: {
    chip: 'bg-risk-watch-bg text-risk-watch',
    border: 'border-l-risk-watch',
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
  const [lastResolved, setLastResolved] = useState<{
    id: string;
    title: string;
    tracking: string;
    shipmentId: string;
    customerTold: boolean;
  } | null>(null);

  const alertsKey = ['alerts', 'open', includeSnoozed];
  const {
    data: pages,
    isLoading,
    error,
    refetch,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteQuery({
    queryKey: alertsKey,
    queryFn: async ({ pageParam }: { pageParam?: string }) => {
      const qs = new URLSearchParams({ status: 'open', limit: '50' });
      if (includeSnoozed) qs.set('include', 'snoozed');
      if (pageParam) qs.set('cursor', pageParam);
      return (await api.get<AlertPage>(`/api/v1/alerts?${qs}`)).data;
    },
    getNextPageParam: (last) => last.nextCursor,
    initialPageParam: undefined as string | undefined,
  });
  const alerts = useMemo(
    () => pages?.pages.flatMap((p) => p.alerts),
    [pages],
  );

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
    onSuccess: (_data, vars) => {
      // Peak-end relief: the resolution moment must FEEL like competence.
      // The resolved card unmounts on refresh, so the proof lives here — a
      // transient banner with cause + customer state, auto-dismissed, never
      // a dead end (links back to the shipment).
      const done = (alerts ?? []).find((a) => a.id === vars.id);
      setLastResolved(
        done
          ? {
              id: done.id,
              title: done.title,
              tracking: done.trackingNumber ?? done.shipmentId,
              shipmentId: done.shipmentId,
              customerTold: done.customerNotified ?? false,
            }
          : null,
      );
      setExpanded(null);
      refresh();
    },
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

  const { data: budget } = useQuery({
    queryKey: ['notify-budget'],
    queryFn: async () =>
      (
        await api.get<{
          interruptHourlyCap: number;
          interruptsUsed: number;
          digestPending: number;
        }>('/api/v1/notify/status')
      ).data,
    staleTime: 30_000,
  });

  // Calibration, from real closure data (never a static claim): share of
  // 30d resolutions that were not false positives/duplicates, plus ETA
  // accuracy. Small samples render as "collecting", never as a percentage
  // that over-promises.
  const { data: calibration } = useQuery({
    queryKey: ['analytics', 'calibration'],
    queryFn: async () =>
      (
        await api.get<{
          usefulPct?: number;
          resolutionSamples?: number;
          etaAccuracyPct?: number;
          etaSamples?: number;
        }>('/api/v1/analytics')
      ).data,
    staleTime: 300_000,
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
          <p className="text-sm text-slate-600">
            Worst first. <span className="font-semibold">{stats.unowned}</span> unowned ·{' '}
            <span className="font-semibold">{stats.breached}</span> past SLA ·{' '}
            <span className="font-semibold">{stats.untold}</span> customer not told
          </p>
          {budget ? (
            <p
              className="mt-1 text-xs text-slate-600"
              role="status"
              aria-live="polite"
              title="Non-interrupting notifications queue into a digest — suppressed never means dropped."
            >
              Interrupt budget: {budget.interruptsUsed}/{budget.interruptHourlyCap} used
              {budget.digestPending > 0 ? ` · ${budget.digestPending} waiting in digest` : ' · digest clear'}
            </p>
          ) : null}
          <CalibrationLine calibration={calibration} />
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

      {lastResolved ? (
        <div
          className="ts-enter mb-4 flex flex-wrap items-center gap-3 rounded-2xl bg-risk-clear-bg p-4 ring-1 ring-risk-clear"
          role="status"
        >
          <span
            aria-hidden="true"
            className="flex h-8 w-8 items-center justify-center rounded-full bg-risk-clear text-lg font-bold text-white"
          >
            ✓
          </span>
          <div className="min-w-0 flex-1">
            <p className="text-sm font-bold text-navy-950">
              Cleared — {lastResolved.title} ({lastResolved.tracking})
            </p>
            <p className="text-xs text-slate-600">
              {lastResolved.customerTold
                ? 'Customer was already notified — nothing left open.'
                : 'Customer has not been told yet — send the update from the shipment.'}
            </p>
          </div>
          <Link
            href={`/shipments/${lastResolved.shipmentId}`}
            className="rounded-xl bg-navy-950 px-3 py-2 text-xs font-semibold text-white"
          >
            Open shipment →
          </Link>
          <button
            type="button"
            onClick={() => setLastResolved(null)}
            aria-label="Dismiss resolution confirmation"
            className="rounded-xl border border-slate-200 px-3 py-2 text-xs font-semibold text-navy-950"
          >
            Dismiss
          </button>
        </div>
      ) : null}

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
        // But "quiet" must prove it is watched, not broken: the trust line
        // names the read-model freshness (real max(updated_at), never a
        // static string), and the empty state always offers a next action.
        <AllClearEmpty
          readModelAt={pages?.pages[0]?.readModelAt}
          stats={stats}
        />
      ) : (
        <>
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
                      {a.riskTier && a.riskTier !== 'clear' && a.riskBreakdown ? (
                        <RiskTooltip
                          score={a.riskScore ?? 0}
                          breakdown={a.riskBreakdown}
                          facts={{
                            staleHours: a.staleHours ?? undefined,
                            valueAtRisk: a.valueAtRisk,
                            customerNotified: a.customerNotified,
                          }}
                          links={{
                            timelineHref: `/shipments/${a.shipmentId}#timeline`,
                            alertsHref: `/shipments/${a.shipmentId}`,
                          }}
                        >
                          <span className="cursor-help font-sans font-bold text-slate-600 underline decoration-dotted underline-offset-2">
                            {RISK_LABELS[a.riskTier]} ({a.riskScore})
                          </span>
                        </RiskTooltip>
                      ) : a.riskTier && a.riskTier !== 'clear' ? (
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
          {hasNextPage && (
            <div className="mt-4 text-center">
              <button
                type="button"
                disabled={isFetchingNextPage}
                onClick={() => void fetchNextPage()}
                className="rounded-xl border border-slate-200 px-5 py-2.5 text-sm font-semibold text-navy-950 hover:bg-slate-50 disabled:opacity-50"
              >
                {isFetchingNextPage ? 'Loading…' : 'Load older exceptions'}
              </button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function CalibrationLine({
  calibration,
}: {
  calibration?: {
    usefulPct?: number;
    resolutionSamples?: number;
    etaAccuracyPct?: number;
    etaSamples?: number;
  };
}) {
  if (!calibration) return null;
  const resN = calibration.resolutionSamples ?? 0;
  const etaN = calibration.etaSamples ?? 0;
  const parts: string[] = [];
  if (calibration.usefulPct !== undefined && resN >= 10) {
    parts.push(`Judged useful ${Math.round(calibration.usefulPct)}% (n=${resN}, 30d)`);
  } else {
    parts.push(`Collecting calibration data (n=${resN} resolutions)`);
  }
  if (calibration.etaAccuracyPct !== undefined && etaN >= 10) {
    parts.push(`ETA ±6h ${Math.round(calibration.etaAccuracyPct)}% (n=${etaN})`);
  } else if (etaN > 0) {
    parts.push(`ETA accuracy collecting (n=${etaN})`);
  }
  return (
    <p
      className="mt-1 text-xs text-slate-500"
      role="status"
      title="From real resolutions and deliveries — small samples say so instead of quoting a percentage."
    >
      {parts.join(' · ')}
    </p>
  );
}

function AllClearEmpty({
  readModelAt,
  stats,
}: {
  readModelAt?: string;
  stats: { total: number };
}) {
  const freshness = readModelAt ? (
    <>
      Read model fresh as of {relativeTime(readModelAt)} · {stats.total}{' '}
      shipment{stats.total === 1 ? '' : 's'} scored.
    </>
  ) : (
    <>First sweep pending — scores land on the next pass.</>
  );
  return (
    <Empty
      message="All clear — no open exceptions."
      detail={freshness}
      actions={
        <>
          <Link
            href="/shipments"
            className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white"
          >
            View shipments
          </Link>
          <Link
            href="/"
            className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold text-navy-950 hover:bg-slate-50"
          >
            Open setup guide →
          </Link>
        </>
      }
    />
  );
}

/**
 * Customer-ready message templates per root cause. Choice overload kills
 * triage: the operator picks a cause, the words are already written in a
 * calm human voice, and they can edit before sending. Never auto-send.
 */
const CAUSE_TEMPLATES: Record<AlertRootCause, string> = {
  documentation: 'A document is missing for your shipment {tracking} ({title}). We have requested it and will confirm clearance as soon as it lands.',
  customs_duty: 'Your shipment {tracking} is held pending a duty payment ({title}). We are clearing it now and will confirm release.',
  customs_processing: 'Your shipment {tracking} is in customs processing ({title}). This usually clears within a day — we are tracking it and will confirm.',
  carrier_delay: 'The carrier reports a delay on your shipment {tracking} ({title}). We are pressing for a revised arrival and will update you.',
  missed_connection: 'Your shipment {tracking} missed its connection ({title}). We are rebooking now and will confirm the new plan.',
  blank_sailing: 'The scheduled sailing for your shipment {tracking} was cancelled by the carrier ({title}). Rebooking is underway — new plan to follow.',
  port_congestion: 'The terminal is congested around your shipment {tracking} ({title}). Arrival may slip slightly — we will confirm the revised window.',
  capacity: 'Capacity is tight on your lane — your shipment {tracking} ({title}) may move to the next departure. We will confirm either way.',
  weather: 'Weather is holding your shipment {tracking} ({title}). Safety first — we will confirm movement as soon as the lane reopens.',
  carrier_error: 'The carrier flagged an error on your shipment {tracking} ({title}). We are on it and will confirm the correction.',
  shipper_delay: 'The shipper delayed handover for {tracking} ({title}). We will confirm once the goods are moving.',
  other: 'An update on your shipment {tracking}: {title}. We are handling it and will confirm the next step shortly.',
};

function fillTemplate(cause: AlertRootCause, tracking: string, title: string): string {
  return (CAUSE_TEMPLATES[cause] ?? CAUSE_TEMPLATES.other)
    .replaceAll('{tracking}', tracking || 'your shipment')
    .replaceAll('{title}', title);
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
  const [carrierTo, setCarrierTo] = useState('');
  const [customerMsg, setCustomerMsg] = useState('');
  const [toast, setToast] = useState<{ message: string; type: 'success' | 'error' } | null>(null);

  const customerUpdate = customerMsg.trim() ||
    `Update on ${alert.trackingNumber ?? 'your shipment'}: ${alert.title} — ${alert.message}`;

  const sendCustomerUpdate = async (tracking: string, title: string, message: string) => {
    try {
      const res = await fetch(`/api/v1/shipments/${alert.shipmentId}/notify`, {
        method: 'POST',
        credentials: 'include',
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

  const emailCarrier = async (tracking: string, title: string, message: string, note: string, to: string) => {
    if (!to.trim()) {
      setToast({ message: 'Type the carrier contact address first', type: 'error' });
      return;
    }
    try {
      const res = await fetch(`/api/v1/shipments/${alert.shipmentId}/email-carrier`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tracking, title, message, note, to: to.trim() }),
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
        <div className={`rounded-lg px-3 py-2 text-xs font-semibold ${toast.type === 'success' ? 'bg-risk-clear-bg text-risk-clear' : 'bg-risk-critical-bg text-risk-critical'}`} role="status">
          {toast.message}
        </div>
      )}
      {/* Cost of inaction, framed honestly (loss aversion without dark
          patterns): what is at stake and how long the clock has run. */}
      <p className="rounded-lg bg-white px-3 py-2 text-xs text-slate-600 ring-1 ring-slate-200">
        {alert.valueAtRisk ? (
          <><span className="font-bold text-navy-950">${Math.round(alert.valueAtRisk).toLocaleString()}</span> at risk · </>
        ) : (
          <>No declared value on file · </>
        )}
        {alert.staleHours && alert.staleHours > 1 ? (
          <>no carrier scan for {Math.round(alert.staleHours)}h · </>
        ) : null}
        {alert.dueAt ? <>SLA {new Date(alert.dueAt) < new Date() ? 'breached' : 'due'} {relativeTime(alert.dueAt)}</> : <>no SLA deadline set</>}
      </p>
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
        <div className="mt-1 flex gap-2">
          <select
            value={cause}
            disabled={busy}
            onChange={(e) => setCause(e.target.value as AlertRootCause)}
            className="w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 text-sm font-normal"
          >
            {ROOT_CAUSES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
          <button
            type="button"
            disabled={busy}
            onClick={() =>
              setCustomerMsg(fillTemplate(cause, alert.trackingNumber ?? '', alert.title))
            }
            title="Fill the customer message from the selected cause — edit before sending, never auto-sent."
            className="shrink-0 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-navy-950 hover:bg-slate-50"
          >
            Use template
          </button>
        </div>
      </label>

      <label className="block text-xs font-semibold text-slate-700">
        Customer message <span className="font-normal text-slate-500">(editable — nothing sends until you press it)</span>
        <textarea
          value={customerMsg}
          onChange={(e) => setCustomerMsg(e.target.value)}
          placeholder="Pick a cause, press “Use template”, then edit…"
          rows={2}
          className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 text-sm font-normal"
        />
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

      <label className="block text-xs font-semibold text-slate-700">
        Carrier contact (email)
        <input
          value={carrierTo}
          onChange={(e) => setCarrierTo(e.target.value)}
          placeholder="ops@carrier.example"
          type="email"
          autoComplete="off"
          className="mt-1 w-full rounded-lg border border-slate-300 bg-white px-2 py-1.5 font-mono text-sm font-normal"
        />
      </label>

      {/* Three primary actions max (Hick's Law at a moment of stress):
          tell the customer, press the carrier, or close with a cause.
          Copy is secondary. One-click sends bypass the interrupt budget BY
          DESIGN — a human pressed the button, and automation never may. */}
      <div className="flex flex-wrap items-center gap-2 pt-1">
        {!alert.customerNotified ? (
          <button
            type="button"
            disabled={busy}
            onClick={() => sendCustomerUpdate(alert.trackingNumber ?? '', alert.title, customerUpdate)}
            className="rounded-xl bg-navy-950 px-4 py-2 text-xs font-semibold text-white disabled:opacity-50"
          >
            Send customer update
          </button>
        ) : null}
        <button
          type="button"
          disabled={busy}
          onClick={() => emailCarrier(alert.trackingNumber ?? '', alert.title, alert.message, note.trim(), carrierTo)}
          className="rounded-xl bg-accent-700 px-4 py-2 text-xs font-semibold text-white hover:bg-accent-700-hover disabled:opacity-50"
        >
          Email carrier agent
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() => onResolve(cause, note.trim())}
          className="rounded-xl border border-slate-300 bg-white px-4 py-2 text-xs font-semibold text-navy-950"
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
          aria-live="polite"
          className="rounded-xl px-3 py-2 text-xs font-semibold text-slate-600 hover:underline"
        >
          Copy update
        </button>
      </div>
      <p className="text-[11px] text-slate-500">
        One-click actions send immediately and bypass the interrupt budget by design — a human pressed them.
        {!alert.customerNotified ? (
          <span className="font-semibold text-slate-600"> Customer has not been told — send the update before closing.</span>
        ) : null}
      </p>
      {failed ? (
        <p role="alert" className="text-xs text-red-600">
          Action failed — try again.
        </p>
      ) : null}
    </div>
  );
}