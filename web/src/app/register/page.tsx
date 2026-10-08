'use client';

import { useState, type FormEvent } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { RequestError } from '@/lib/api';

export default function RegisterPage() {
  const { register } = useAuth();
  const router = useRouter();
  const [org, setOrg] = useState('');
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await register(org, name, email, password);
      router.push('/');
    } catch (err) {
      setError(err instanceof RequestError ? err.message : 'Registration failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="auth-layout">
      <aside className="auth-story" aria-label="TrackSphere logistics platform">
        <div className="auth-brand">
          <span className="auth-brand-mark" aria-hidden="true">T</span>
          <span>Track<span>Sphere</span></span>
        </div>
        <div className="auth-story-copy">
          <span className="auth-kicker">Logistics intelligence</span>
          <h2>Every movement.<br />In clear view.</h2>
          <p>One calm, connected place to see what is moving, what needs attention, and what comes next.</p>
        </div>
        <div className="auth-route-art" aria-hidden="true">
          <span className="auth-route-point auth-route-point--one" />
          <span className="auth-route-point auth-route-point--two" />
          <span className="auth-route-point auth-route-point--three" />
        </div>
        <p className="auth-story-foot">A better vantage point for global operations.</p>
      </aside>
      <section className="auth-form-wrap">
      <div className="auth-card w-full max-w-md rounded-3xl bg-white p-8 shadow-2xl">
        <div className="mb-6 text-center">
          <div className="mx-auto mb-3 flex h-11 w-11 items-center justify-center rounded-xl bg-accent-500 text-lg font-bold text-white">
            T
          </div>
          <h1 className="text-xl font-extrabold text-navy-950">Create your organization</h1>
          <p className="mt-1 text-sm text-slate-500">
            14-day trial · no credit card · cancel anytime
          </p>
        </div>

        <form onSubmit={onSubmit} className="space-y-4">
          <Field label="Organization">
            <input required value={org} onChange={(e) => setOrg(e.target.value)} placeholder="Acme Logistics" />
          </Field>
          <Field label="Your name">
            <input required value={name} onChange={(e) => setName(e.target.value)} placeholder="Dara Ops" />
          </Field>
          <Field label="Work email">
            <input
              type="email"
              required
              autoComplete="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@company.com"
            />
          </Field>
          <Field label="Password">
            <input
              type="password"
              required
              minLength={8}
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="At least 8 characters"
            />
          </Field>
          {error ? <p className="text-sm text-red-600">{error}</p> : null}
          <button
            disabled={busy}
            className="w-full rounded-xl bg-accent-500 py-3 font-semibold text-white hover:bg-accent-600 disabled:opacity-50"
          >
            {busy ? 'Creating…' : 'Create organization'}
          </button>
        </form>

        <p className="mt-6 text-center text-sm text-slate-500">
          Already have an account?{' '}
          <Link href="/login" className="font-semibold text-accent-500 hover:underline">
            Sign in
          </Link>
        </p>
      </div>
      </section>
    </main>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="mb-1 block text-xs font-semibold text-slate-500">{label}</label>
      <div className="[&_input]:w-full [&_input]:rounded-xl [&_input]:border [&_input]:border-slate-300 [&_input]:px-4 [&_input]:py-3 [&_input]:focus:border-accent-500 [&_input]:focus:outline-none">
        {children}
      </div>
    </div>
  );
}
