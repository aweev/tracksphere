'use client';

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState, type FormEvent } from 'react';
import { RequireAuth } from '@/components/RequireAuth';
import { Card, Empty } from '@/components/ui';
import { api, authApi, type ApiKey, type Billing, type SessionItem, type TeamMember, type User, type WebhookEndpoint } from '@/lib/api';
import { useAuth } from '@/lib/auth';

export default function SettingsPage() {
  return (
    <RequireAuth>
      <SettingsBody />
    </RequireAuth>
  );
}

function SettingsBody() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'owner';
  const isOwner = user?.role === 'owner';
  return (
    <div className="space-y-6 p-4 md:p-8">
      <header>
        <h1 className="text-2xl font-extrabold text-navy-950">Settings</h1>
        <p className="text-sm text-slate-500">Profile, security, team, developers</p>
      </header>
      <ProfileCard user={user} />
      <BillingCard />
      <MfaCard />
      <SessionsCard />
      <PasswordCard />
      {isAdmin ? (
        <>
          <BrandingCard />
          <TeamCard />
          <ApiKeysCard />
          <WebhooksCard />
        </>
      ) : (
        <Card title="Team & developers">
          <Empty message="Team, branding, API keys and webhooks need an admin or owner role." />
        </Card>
      )}
      {isOwner ? (
        <>
          <ComplianceCard />
          <DangerZone />
        </>
      ) : null}
    </div>
  );
}

function ProfileCard({ user }: { user: User | null }) {
  if (!user) return null;
  return (
    <Card title="Profile">
      <dl className="grid gap-2 text-sm md:grid-cols-2">
        <div><dt className="text-xs uppercase text-slate-400">Name</dt><dd className="font-semibold">{user.name || '—'}</dd></div>
        <div><dt className="text-xs uppercase text-slate-400">Email</dt><dd className="font-mono">{user.email}</dd></div>
        <div><dt className="text-xs uppercase text-slate-400">Role</dt><dd className="capitalize">{user.role}</dd></div>
        <div><dt className="text-xs uppercase text-slate-400">MFA</dt><dd>{user.totpEnabled ? 'Enabled' : 'Disabled'}</dd></div>
      </dl>
    </Card>
  );
}

function BillingCard() {
  const { data } = useQuery({
    queryKey: ['billing'],
    queryFn: async () => (await api.get<Billing>('/api/v1/billing')).data,
  });
  if (!data) return null;
  const [upgradeMsg, setUpgradeMsg] = useState('');
  const upgrade = async (plan: 'growth' | 'enterprise') => {
    setUpgradeMsg('');
    try {
      const res = await api.post<{ url: string }>('/api/v1/billing/checkout', { plan });
      window.location.href = res.data.url;
    } catch (e) {
      setUpgradeMsg(e instanceof Error ? e.message : 'Checkout failed.');
    }
  };
  const rows: Array<[string, number, number]> = [
    ['Shipments tracked', data.usage.shipments, data.limits.shipments],
    ['Team seats', data.usage.seats, data.limits.seats],
    ['API keys', data.usage.apiKeys, data.limits.apiKeys],
    ['Webhook endpoints', data.usage.endpoints, data.limits.endpoints],
  ];
  return (
    <Card
      title={`Plan: ${data.plan}`}
      action={
        <span className={`rounded-full px-2 py-0.5 text-[11px] font-bold ${data.trialActive ? 'bg-emerald-100 text-emerald-700' : 'bg-amber-100 text-amber-800'}`}>
          {data.trialActive ? `Trial · ${data.trialDaysLeft}d left` : 'Trial ended'}
        </span>
      }
    >
      <p className="mb-3 text-xs text-slate-500">
        {data.trialActive
          ? `No credit card on file — trial ends ${new Date(data.trialEndsAt).toLocaleDateString()}.`
          : 'Trial ended — contact sales to upgrade to growth or enterprise.'}
      </p>
      <ul className="space-y-2">
        {rows.map(([label, used, limit]) => (
          <li key={label}>
            <div className="flex justify-between text-sm">
              <span className="text-slate-600">{label}</span>
              <span className="font-mono text-navy-950">
                {used}{limit < 0 ? '' : ` / ${limit}`}
              </span>
            </div>
            {limit > 0 ? (
              <div className="mt-1 h-1.5 rounded-full bg-slate-100" role="progressbar"
                aria-valuenow={used} aria-valuemin={0} aria-valuemax={limit} aria-label={label}>
                <div
                  className={`h-1.5 rounded-full ${used / limit > 0.85 ? 'bg-red-500' : 'bg-accent-500'}`}
                  style={{ width: `${Math.min(100, Math.round((used / limit) * 100))}%` }}
                />
              </div>
            ) : null}
          </li>
        ))}
      </ul>
      {data.plan === 'starter' ? (
        <div className="mt-4 flex flex-wrap gap-2">
          <button type="button" onClick={() => void upgrade('growth')}
            className="rounded-xl bg-accent-500 px-4 py-2 text-sm font-semibold text-white">
            Upgrade to growth
          </button>
          <button type="button" onClick={() => void upgrade('enterprise')}
            className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold">
            Talk enterprise
          </button>
        </div>
      ) : null}
      {upgradeMsg ? <p role="status" className="mt-2 text-sm text-slate-600">{upgradeMsg}</p> : null}
    </Card>
  );
}

function MfaCard() {
  const qc = useQueryClient();
  const [secret, setSecret] = useState<{ secret: string; otpauthURI: string } | null>(null);
  const [code, setCode] = useState('');
  const [msg, setMsg] = useState('');
  const enroll = useMutation({
    mutationFn: () => authApi.mfaEnroll(),
    onSuccess: (r) => setSecret(r.data),
    onError: () => setMsg('Enrollment failed.'),
  });
  const enable = useMutation({
    mutationFn: (c: string) => authApi.mfaEnable(c),
    onSuccess: () => {
      setMsg('MFA enabled.');
      setSecret(null);
      setCode('');
      void qc.invalidateQueries({ queryKey: ['me'] });
    },
    onError: () => setMsg('Incorrect code.'),
  });
  const disable = useMutation({
    mutationFn: (c: string) => authApi.mfaDisable(c),
    onSuccess: () => {
      setMsg('MFA disabled.');
      setCode('');
      void qc.invalidateQueries({ queryKey: ['me'] });
    },
    onError: () => setMsg('Incorrect code.'),
  });
  const submit = (fn: (c: string) => void) => (e: FormEvent) => {
    e.preventDefault();
    setMsg('');
    fn(code);
  };
  return (
    <Card title="Two-factor authentication (TOTP)">
      <div className="flex flex-wrap gap-2">
        <button type="button" onClick={() => enroll.mutate()}
          className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          {enroll.isPending ? '…' : 'Enroll (new secret)'}
        </button>
      </div>
      {secret ? (
        <div className="mt-3 rounded-xl bg-slate-50 p-4 text-sm">
          <p className="font-semibold">Scan into your authenticator app, then confirm:</p>
          <p className="mt-1 break-all font-mono text-xs">{secret.otpauthURI}</p>
          <p className="mt-1 font-mono text-xs text-slate-500">secret: {secret.secret}</p>
        </div>
      ) : null}
      <form onSubmit={submit((c) => enable.mutate(c))} className="mt-3 flex gap-2">
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="6-digit code"
          aria-label="TOTP code" autoComplete="one-time-code" inputMode="numeric"
          className="w-40 rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <button type="submit" className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold">
          Enable
        </button>
        <button type="button" onClick={() => disable.mutate(code)}
          className="rounded-xl border border-red-200 px-4 py-2 text-sm font-semibold text-red-600">
          Disable
        </button>
      </form>
      {msg ? <p role="status" className="mt-2 text-sm text-slate-600">{msg}</p> : null}
    </Card>
  );
}

function SessionsCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['sessions'],
    queryFn: async () => (await api.get<SessionItem[]>('/api/v1/auth/sessions')).data,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/auth/sessions/${id}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['sessions'] }),
  });
  return (
    <Card title="Sessions">
      {(data ?? []).length === 0 ? <Empty message="No sessions." /> : (
        <ul className="divide-y divide-slate-100">
          {(data ?? []).map((s) => (
            <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
              <div>
                <span className="font-mono text-xs">{s.id.slice(0, 8)}…</span>
                {s.current ? <span className="ml-2 rounded-full bg-emerald-100 px-2 py-0.5 text-[11px] font-bold text-emerald-700">CURRENT</span> : null}
                <div className="text-xs text-slate-500">
                  {s.ip || 'unknown ip'} · last seen {new Date(s.lastSeenAt).toLocaleString()}
                </div>
              </div>
              {!s.current ? (
                <button type="button" onClick={() => revoke.mutate(s.id)}
                  className="rounded-lg border border-red-200 px-3 py-1.5 text-xs font-semibold text-red-600">
                  Revoke
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function PasswordCard() {
  const [cur, setCur] = useState('');
  const [next, setNext] = useState('');
  const [msg, setMsg] = useState('');
  const change = useMutation({
    mutationFn: () => api.post('/api/v1/auth/password/change', { currentPassword: cur, newPassword: next }),
    onSuccess: () => { setMsg('Password changed. Other sessions revoked.'); setCur(''); setNext(''); },
    onError: (e: unknown) => setMsg(e instanceof Error ? e.message : 'Change failed.'),
  });
  return (
    <Card title="Password">
      <form onSubmit={(e) => { e.preventDefault(); setMsg(''); change.mutate(); }} className="flex flex-wrap gap-2">
        <input type="password" value={cur} onChange={(e) => setCur(e.target.value)} placeholder="Current password"
          aria-label="Current password" autoComplete="current-password"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <input type="password" value={next} onChange={(e) => setNext(e.target.value)} placeholder="New password (8+ chars)"
          aria-label="New password" autoComplete="new-password"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <button type="submit" className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          {change.isPending ? '…' : 'Change'}
        </button>
      </form>
      {msg ? <p role="status" className="mt-2 text-sm text-slate-600">{msg}</p> : null}
    </Card>
  );
}

function BrandingCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['branding'],
    queryFn: async () => (await api.get<{ company: string; color: string; logo: string; support: string }>('/api/v1/branding')).data,
  });
  const [form, setForm] = useState({ company: '', color: '#ff6b00', logo: '', support: '' });
  useEffect(() => {
    if (data) {
      setForm({ company: data.company ?? '', color: data.color || '#ff6b00', logo: data.logo ?? '', support: data.support ?? '' });
    }
  }, [data]);
  const [msg, setMsg] = useState('');
  const save = useMutation({
    mutationFn: () => api.put('/api/v1/branding', form),
    onSuccess: () => { setMsg('Brand saved — the public portal picks it up immediately.'); void qc.invalidateQueries({ queryKey: ['branding'] }); },
    onError: (e: unknown) => setMsg(e instanceof Error ? e.message : 'Save failed.'),
  });
  return (
    <Card title="White-label portal">
      <div className="grid gap-2 md:grid-cols-2">
        <input value={form.company} onChange={(e) => setForm({ ...form, company: e.target.value })}
          placeholder="Company name" aria-label="Company name"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <input value={form.support} onChange={(e) => setForm({ ...form, support: e.target.value })}
          placeholder="Support email" aria-label="Support email"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <input value={form.color} onChange={(e) => setForm({ ...form, color: e.target.value })}
          placeholder="#ff6b00" aria-label="Primary color"
          className="rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <input value={form.logo} onChange={(e) => setForm({ ...form, logo: e.target.value })}
          placeholder="Logo image URL" aria-label="Logo URL"
          className="rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
      </div>
      <button type="button" onClick={() => { setMsg(''); save.mutate(); }}
        className="mt-3 rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
        Save brand
      </button>
      {msg ? <p role="status" className="mt-2 text-sm text-slate-600">{msg}</p> : null}
    </Card>
  );
}

function TeamCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['team'],
    queryFn: async () => (await api.get<TeamMember[]>('/api/v1/team')).data,
  });
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [role, setRole] = useState('member');
  const [once, setOnce] = useState('');
  const invite = useMutation({
    mutationFn: () => api.post<{ tempPassword: string }>('/api/v1/team/invite', { email, name, role }),
    onSuccess: (r) => {
      setOnce(`Temp password for ${email}: ${r.data.tempPassword} — share once, then clear.`);
      setEmail(''); setName('');
      void qc.invalidateQueries({ queryKey: ['team'] });
    },
  });
  const deactivate = useMutation({
    mutationFn: (id: string) => api.post(`/api/v1/team/${id}/deactivate`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['team'] }),
  });
  return (
    <Card title="Team">
      <form onSubmit={(e) => { e.preventDefault(); invite.mutate(); }} className="mb-4 flex flex-wrap gap-2">
        <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" aria-label="Invite email"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name" aria-label="Invite name"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <select value={role} onChange={(e) => setRole(e.target.value)} aria-label="Invite role"
          className="rounded-xl border border-slate-300 px-3 py-2 text-sm">
          <option value="member">Member</option>
          <option value="admin">Admin</option>
        </select>
        <button type="submit" className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          Invite
        </button>
      </form>
      {once ? (
        <p role="status" className="mb-3 rounded-xl bg-amber-50 p-3 font-mono text-xs text-amber-800">
          {once}
          <button type="button" onClick={() => setOnce('')} className="ml-2 font-sans font-semibold underline">clear</button>
        </p>
      ) : null}
      <ul className="divide-y divide-slate-100">
        {(data ?? []).map((m) => (
          <li key={m.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
            <div>
              <span className="font-semibold">{m.name || m.email}</span>
              <span className="ml-2 font-mono text-xs text-slate-500">{m.email}</span>
              <span className="ml-2 text-xs capitalize text-slate-400">{m.role}{m.active ? '' : ' · deactivated'}</span>
            </div>
            {m.active ? (
              <button type="button" onClick={() => deactivate.mutate(m.id)}
                className="rounded-lg border border-red-200 px-3 py-1.5 text-xs font-semibold text-red-600">
                Deactivate
              </button>
            ) : null}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function ApiKeysCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['apikeys'],
    queryFn: async () => (await api.get<ApiKey[]>('/api/v1/apikeys')).data,
  });
  const [name, setName] = useState('');
  const [once, setOnce] = useState('');
  const create = useMutation({
    mutationFn: () => api.post<{ key: string; prefix: string }>('/api/v1/apikeys', { name, role: 'member' }),
    onSuccess: (r) => {
      setOnce(`Key ${r.data.prefix}: ${r.data.key} — copy now, never shown again.`);
      setName('');
      void qc.invalidateQueries({ queryKey: ['apikeys'] });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.post(`/api/v1/apikeys/${id}/revoke`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['apikeys'] }),
  });
  return (
    <Card title="API keys (B2B)">
      <form onSubmit={(e) => { e.preventDefault(); create.mutate(); }} className="mb-3 flex gap-2">
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Key name (e.g. Shopify sync)"
          aria-label="API key name" className="rounded-xl border border-slate-300 px-3 py-2 text-sm" />
        <button type="submit" className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          Mint key
        </button>
      </form>
      {once ? (
        <p role="status" className="mb-3 break-all rounded-xl bg-amber-50 p-3 font-mono text-xs text-amber-800">
          {once}
          <button type="button" onClick={() => setOnce('')} className="ml-2 font-sans font-semibold underline">clear</button>
        </p>
      ) : null}
      <ul className="divide-y divide-slate-100">
        {(data ?? []).map((k) => (
          <li key={k.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
            <div>
              <span className="font-semibold">{k.name}</span>
              <span className="ml-2 font-mono text-xs text-slate-500">{k.prefix}…</span>
              <span className="ml-2 text-xs capitalize text-slate-400">{k.role}{k.revoked ? ' · revoked' : ''}</span>
            </div>
            {!k.revoked ? (
              <button type="button" onClick={() => revoke.mutate(k.id)}
                className="rounded-lg border border-red-200 px-3 py-1.5 text-xs font-semibold text-red-600">
                Revoke
              </button>
            ) : null}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function WebhooksCard() {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['webhooks-out'],
    queryFn: async () => (await api.get<WebhookEndpoint[]>('/api/v1/webhooks/out')).data,
  });
  const [url, setUrl] = useState('');
  const [once, setOnce] = useState('');
  const create = useMutation({
    mutationFn: () => api.post<{ secret: string }>('/api/v1/webhooks/out', { url }),
    onSuccess: (r) => {
      setOnce(`Endpoint secret: ${r.data.secret} — copy now, never shown again.`);
      setUrl('');
      void qc.invalidateQueries({ queryKey: ['webhooks-out'] });
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/webhooks/out/${id}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['webhooks-out'] }),
  });
  return (
    <Card title="Outbound webhooks">
      <form onSubmit={(e) => { e.preventDefault(); create.mutate(); }} className="mb-3 flex gap-2">
        <input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://…"
          aria-label="Webhook URL" className="flex-1 rounded-xl border border-slate-300 px-3 py-2 font-mono text-sm" />
        <button type="submit" className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
          Add endpoint
        </button>
      </form>
      {once ? (
        <p role="status" className="mb-3 break-all rounded-xl bg-amber-50 p-3 font-mono text-xs text-amber-800">
          {once}
          <button type="button" onClick={() => setOnce('')} className="ml-2 font-sans font-semibold underline">clear</button>
        </p>
      ) : null}
      <ul className="divide-y divide-slate-100">
        {(data ?? []).map((w) => (
          <li key={w.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5 text-sm">
            <div className="break-all font-mono text-xs">{w.url}
              <span className="ml-2 font-sans text-slate-400">{w.events.join(', ')}</span>
            </div>
            <button type="button" onClick={() => remove.mutate(w.id)}
              className="rounded-lg border border-red-200 px-3 py-1.5 text-xs font-semibold text-red-600">
              Delete
            </button>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function ComplianceCard() {
  const [msg, setMsg] = useState('');
  const download = async () => {
    setMsg('');
    try {
      const res = await fetch('/api/v1/compliance/evidence', { credentials: 'include' });
      if (!res.ok) throw new Error('Evidence export failed');
      const blob = await res.blob();
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'soc2-evidence.json';
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) {
      setMsg(e instanceof Error ? e.message : 'Export failed.');
    }
  };
  return (
    <Card title="Compliance (SOC 2)">
      <p className="mb-3 text-xs text-slate-500">
        One-click evidence pack: controls, MFA coverage, sessions, 90-day auth stats, region, retention windows.
      </p>
      <button type="button" onClick={() => void download()}
        className="rounded-xl bg-navy-950 px-4 py-2 text-sm font-semibold text-white">
        Download evidence pack
      </button>
      {msg ? <p role="status" className="mt-2 text-sm text-slate-600">{msg}</p> : null}
    </Card>
  );
}

function DangerZone() {
  const [confirm, setConfirm] = useState('');
  const [msg, setMsg] = useState('');
  const [busy, setBusy] = useState(false);
  const download = async () => {
    setMsg('');
    try {
      const res = await fetch('/api/v1/account/export', { credentials: 'include' });
      if (!res.ok) throw new Error('Export failed');
      const blob = await res.blob();
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'tracksphere-export.json';
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) {
      setMsg(e instanceof Error ? e.message : 'Export failed.');
    }
  };
  const erase = async () => {
    if (confirm !== 'ERASE') {
      setMsg('Type ERASE to confirm.');
      return;
    }
    setBusy(true);
    try {
      await api.del('/api/v1/account');
      window.location.href = '/register';
    } catch (e) {
      setMsg(e instanceof Error ? e.message : 'Erase failed.');
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Danger zone (GDPR)">
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" onClick={() => void download()}
          className="rounded-xl border border-slate-200 px-4 py-2 text-sm font-semibold">
          Export my data (Art. 20)
        </button>
        <input value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder="Type ERASE"
          aria-label="Erase confirmation"
          className="rounded-xl border border-red-200 px-3 py-2 font-mono text-sm" />
        <button type="button" onClick={() => void erase()} disabled={busy}
          className="rounded-xl bg-red-600 px-4 py-2 text-sm font-semibold text-white disabled:opacity-50">
          Erase organization
        </button>
      </div>
      {msg ? <p role="status" className="mt-2 text-sm text-slate-600">{msg}</p> : null}
      <p className="mt-2 text-xs text-slate-500">
        Erasing deletes the tenant and everything in it — shipments, documents, keys, sessions. Irreversible.
      </p>
    </Card>
  );
}
