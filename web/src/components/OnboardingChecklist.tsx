'use client';

import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import Link from 'next/link';
import { Card } from '@/components/ui';
import { api, type AlertPage, type DashboardStats, type Shipment } from '@/lib/api';
import { useAuth } from '@/lib/auth';

/**
 * First-run setup guide: signup → first real exception in under 10 minutes.
 *
 * Psychology: Zeigarnik (open loops pull completion — "3/5 done"), Endowed
 * Progress (steps you already earned stay checked), Cognitive Load (one
 * checklist, one next action highlighted, everything else collapses).
 *
 * Honesty rules: every step is derived from REAL queries (shipments exist,
 * portal published, alerts open), never from "I clicked it". Dismissal is
 * per-tenant in localStorage; completion hides the card for good.
 */
const DISMISS_PREFIX = 'ts-ob-dismissed:';
const SAW_ALERT_PREFIX = 'ts-ob-saw-alert:';

function readFlag(prefix: string, tenant: string | undefined): boolean {
  if (!tenant || typeof localStorage === 'undefined') return false;
  try {
    return localStorage.getItem(prefix + tenant) === '1';
  } catch {
    return false;
  }
}

function writeFlag(prefix: string, tenant: string | undefined) {
  if (!tenant || typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(prefix + tenant, '1');
  } catch {
    /* private mode — checklist simply reappears */
  }
}

export function OnboardingChecklist() {
  const { user } = useAuth();
  const tenant = user?.tenantId;
  const admin = user?.role === 'owner' || user?.role === 'admin';
  const qc = useQueryClient();
  const [dismissed, setDismissed] = useState(() => readFlag(DISMISS_PREFIX, tenant));
  const [simError, setSimError] = useState('');

  const { data: stats } = useQuery({
    queryKey: ['dashboard'],
    queryFn: async () => (await api.get<DashboardStats>('/api/v1/dashboard')).data,
  });
  const { data: sample } = useQuery({
    queryKey: ['shipments', 'onboarding-sample'],
    queryFn: async () =>
      (await api.get<Shipment[]>('/api/v1/shipments?limit=25&sort=recent')).data,
  });
  const { data: alertsPage } = useQuery({
    queryKey: ['alerts', 'open', 'onboarding'],
    queryFn: async () =>
      (await api.get<AlertPage>('/api/v1/alerts?status=open&limit=1')).data,
  });
  const { data: carriers } = useQuery({
    queryKey: ['carriers', 'onboarding'],
    queryFn: async () =>
      (
        await api.get<{
          supported: string[];
          credentials: { id: string; active: boolean }[];
        }>('/api/v1/carriers')
      ).data,
  });

  const totalShipments = (stats?.activeShipments ?? 0) + (stats?.deliveredShipments ?? 0);
  const hasShipments = totalShipments > 0;
  const portalLive = (sample ?? []).some((s) => s.isPublic);
  const openAlerts = alertsPage?.alerts.length ?? 0;
  const sawAlert = openAlerts > 0 || readFlag(SAW_ALERT_PREFIX, tenant);
  if (openAlerts > 0) writeFlag(SAW_ALERT_PREFIX, tenant);

  const simulate = useMutation({
    mutationFn: async (shipmentId: string) =>
      api.post(`/api/v1/shipments/${shipmentId}/events`, {
        code: 'CUSTOMS_HOLD',
        description: 'Test exception fired from the setup guide.',
        location: 'Setup simulator',
        status: 'exception',
      }),
    onSuccess: () => {
      setSimError('');
      writeFlag(SAW_ALERT_PREFIX, tenant);
      void qc.invalidateQueries({ queryKey: ['alerts'] });
      void qc.invalidateQueries({ queryKey: ['dashboard'] });
      void qc.invalidateQueries({ queryKey: ['shipments'] });
    },
    onError: (e: unknown) => setSimError(e instanceof Error ? e.message : 'Simulator failed.'),
  });

  if (dismissed) return null;

  const steps = [
    {
      done: (carriers?.credentials ?? []).some((c) => c.active),
      title: 'Connect a carrier credentials set',
      hint: 'One key gives the poller a real feed — or push carrier webhooks straight in.',
      action: admin ? (
        <Link href="/integrations" className={OB_ACTION}>
          Open integrations →
        </Link>
      ) : (
        <span className="text-xs text-slate-500">Ask an admin to add credentials.</span>
      ),
    },
    {
      done: hasShipments,
      title: 'Add your first shipment',
      hint: 'Create one manually, or connect a carrier so webhooks flow in.',
      action: (
        <Link href="/shipments" className={OB_ACTION}>
          Go to shipments →
        </Link>
      ),
    },
    {
      done: portalLive,
      title: 'Publish the customer portal',
      hint: 'Flip one shipment public — your customer sees your brand, not ours.',
      action: (sample ?? [])[0] ? (
        <Link href={`/shipments/${(sample ?? [])[0].id}`} className={OB_ACTION}>
          Open your first shipment →
        </Link>
      ) : (
        <span className="text-xs text-slate-500">Needs step 1 first.</span>
      ),
    },
    {
      done: sawAlert,
      title: 'Trigger a live exception',
      hint: 'Fires a real customs-hold scan through the event engine — a genuine critical alert, not a demo banner.',
      action:
        (sample ?? [])[0] && admin ? (
          <button
            type="button"
            disabled={simulate.isPending}
            onClick={() => simulate.mutate((sample ?? [])[0].id)}
            className={OB_ACTION}
          >
            {simulate.isPending ? 'Firing…' : '▶ Simulate customs hold'}
          </button>
        ) : (sample ?? [])[0] ? (
          <span className="text-xs text-slate-500">Ask an admin to run the simulator.</span>
        ) : (
          <span className="text-xs text-slate-500">Needs step 1 first.</span>
        ),
    },
    {
      done: sawAlert && openAlerts === 0,
      title: 'Resolve it with a root cause',
      hint: 'Triage the queue: assign, notify, close with a cause. That cause feeds carrier scorecards.',
      action: (
        <Link href="/exceptions" className={OB_ACTION}>
          Open exception queue →
        </Link>
      ),
    },
  ];
  const doneCount = steps.filter((s) => s.done).length;

  if (doneCount === steps.length) return null;

  const nextIdx = steps.findIndex((s) => !s.done);
  return (
    <Card
      title={`Setup guide — ${doneCount}/${steps.length} done`}
      action={
        <button
          type="button"
          onClick={() => {
            writeFlag(DISMISS_PREFIX, tenant);
            setDismissed(true);
          }}
          className="text-xs font-semibold text-slate-500 hover:underline"
          aria-label="Dismiss setup guide"
        >
          Dismiss
        </button>
      }
    >
      <ol className="space-y-3" aria-label="Setup progress">
        {steps.map((s, i) => (
          <li
            key={s.title}
            className={`flex items-start gap-3 rounded-xl p-3 ${
              i === nextIdx ? 'bg-slate-50 ring-1 ring-slate-200' : ''
            }`}
            aria-current={i === nextIdx ? 'step' : undefined}
          >
            <span
              aria-hidden="true"
              className={`mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-bold ${
                s.done ? 'bg-risk-clear text-white' : 'bg-slate-200 text-slate-600'
              }`}
            >
              {s.done ? '✓' : i + 1}
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-sm font-bold text-navy-950">
                {s.title}{' '}
                {s.done ? (
                  <span className="sr-only">(done)</span>
                ) : i === nextIdx ? (
                  <span className="ml-1 rounded-full bg-accent-700 px-2 py-0.5 text-[10px] font-bold uppercase text-white">
                    Next
                  </span>
                ) : null}
              </p>
              <p className="text-xs text-slate-600">{s.hint}</p>
              {!s.done && i === nextIdx ? <div className="mt-1.5">{s.action}</div> : null}
            </div>
          </li>
        ))}
      </ol>
      {simError ? (
        <p role="alert" className="mt-2 text-xs text-red-700">
          {simError}
        </p>
      ) : null}
    </Card>
  );
}

const OB_ACTION =
  'inline-block rounded-xl bg-navy-950 px-3.5 py-2 text-xs font-semibold text-white hover:bg-navy-900 focus-visible:ring-2 focus-visible:ring-accent-500';
