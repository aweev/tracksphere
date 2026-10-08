'use client';

import { useEffect, useState, type FormEvent } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { RequestError } from '@/lib/api';

export default function LoginPage() {
  const { login, verifyMfa, challenge, ssoChallenge, startSsoChallenge, clearChallenge } =
    useAuth();
  const router = useRouter();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [sso, setSso] = useState<{ google: boolean; microsoft: boolean } | null>(null);

  // Landed here from the SSO callback: the IdP accepted the user but the
  // account has MFA, so the API issued a challenge into an HttpOnly cookie and
  // redirected here instead of minting a session. Show the code prompt without
  // ever holding the challenge token in script.
  useEffect(() => {
    if (new URLSearchParams(window.location.search).get('mfa') === '1') {
      startSsoChallenge();
    }
  }, [startSsoChallenge]);

  useEffect(() => {
    fetch('/api/v1/auth/sso/status')
      .then((r) => (r.ok ? r.json() : null))
      .then((body) => {
        if (body?.data) setSso(body.data);
      })
      .catch(() => undefined);
  }, []);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      const result = await login(email, password);
      if (result === 'ok') router.push('/');
    } catch (err) {
      setError(err instanceof RequestError ? err.message : 'Sign in failed');
    } finally {
      setBusy(false);
    }
  };

  const onMfa = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await verifyMfa(code);
      router.push('/');
    } catch (err) {
      setError(err instanceof RequestError ? err.message : 'Verification failed');
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
          <h1 className="text-xl font-extrabold text-navy-950">
            Track<span className="text-accent-500">Sphere</span>
          </h1>
          <p className="mt-1 text-sm text-slate-500">Sign in to the control tower</p>
        </div>

        {(challenge || ssoChallenge) ? (
          <form onSubmit={onMfa} className="space-y-4">
            <p className="rounded-xl bg-amber-50 p-3 text-sm text-amber-800">
              Two-factor verification required. Enter the 6-digit code from your authenticator app.
            </p>
            <input
              autoFocus
              inputMode="numeric"
              maxLength={6}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
              placeholder="123456"
              className="w-full rounded-xl border border-slate-300 px-4 py-3 text-center font-mono text-lg tracking-widest focus:border-accent-500 focus:outline-none"
            />
            {error ? <p className="text-sm text-red-600">{error}</p> : null}
            <button
              disabled={busy || code.length !== 6}
              className="w-full rounded-xl bg-navy-950 py-3 font-semibold text-white hover:bg-navy-900 disabled:opacity-50"
            >
              Verify
            </button>
            <button
              type="button"
              onClick={() => {
                clearChallenge();
                setCode('');
                setError('');
              }}
              className="w-full text-center text-xs font-semibold text-slate-400 hover:text-slate-600"
            >
              Back to sign in
            </button>
          </form>
        ) : (
          <form onSubmit={onSubmit} className="space-y-4">
            <div>
              <label className="mb-1 block text-xs font-semibold text-slate-500">Email</label>
              <input
                type="email"
                required
                autoComplete="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="w-full rounded-xl border border-slate-300 px-4 py-3 focus:border-accent-500 focus:outline-none"
                placeholder="you@company.com"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-semibold text-slate-500">Password</label>
              <input
                type="password"
                required
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full rounded-xl border border-slate-300 px-4 py-3 focus:border-accent-500 focus:outline-none"
                placeholder="••••••••"
              />
            </div>
            {error ? <p className="text-sm text-red-600">{error}</p> : null}
            <button
              disabled={busy}
              className="w-full rounded-xl bg-accent-500 py-3 font-semibold text-white hover:bg-accent-600 disabled:opacity-50"
            >
              {busy ? 'Signing in…' : 'Sign in'}
            </button>
            {sso && (sso.google || sso.microsoft) ? (
              <div className="space-y-2 pt-1">
                <div className="flex items-center gap-3 text-xs text-slate-400">
                  <span className="h-px flex-1 bg-slate-200" /> or <span className="h-px flex-1 bg-slate-200" />
                </div>
                {sso.google ? (
                  <a href="/api/v1/auth/sso/google"
                    className="block w-full rounded-xl border border-slate-300 py-3 text-center font-semibold text-navy-950 hover:border-accent-500">
                    Continue with Google
                  </a>
                ) : null}
                {sso.microsoft ? (
                  <a href="/api/v1/auth/sso/microsoft"
                    className="block w-full rounded-xl border border-slate-300 py-3 text-center font-semibold text-navy-950 hover:border-accent-500">
                    Continue with Microsoft
                  </a>
                ) : null}
              </div>
            ) : null}
          </form>
        )}

        <p className="mt-6 text-center text-sm text-slate-500">
          New here?{' '}
          <Link href="/register" className="font-semibold text-accent-500 hover:underline">
            Create an organization
          </Link>
        </p>
      </div>
      </section>
    </main>
  );
}
