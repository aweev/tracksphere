'use client';

import { useRouter } from 'next/navigation';
import { useEffect, type ReactNode } from 'react';
import { useAuth } from '@/lib/auth';
import { Shell } from '@/components/Shell';

/** Redirects to /login when no session; wraps content in the control-tower shell. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (!loading && !user) router.replace('/login');
  }, [loading, user, router]);

  if (loading || !user) {
    return (
      <div className="flex h-full items-center justify-center text-slate-400">Loading…</div>
    );
  }
  return <Shell>{children}</Shell>;
}
