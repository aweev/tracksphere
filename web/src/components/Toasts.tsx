'use client';

import Link from 'next/link';
import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';

export interface Toast {
  id: number;
  title: string;
  body?: string;
  tone: 'info' | 'alert' | 'success';
  href?: string;
  action?: string;
}

interface ToastCtx {
  push: (t: Omit<Toast, 'id'>) => void;
  dismiss: (id: number) => void;
}

const Ctx = createContext<ToastCtx | null>(null);

export function useToasts(): ToastCtx {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error('useToasts must be used inside ToastProvider');
  return ctx;
}

/** Toast host: fixed stack + aria-live announcements for SSE arrivals. */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const idRef = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (t: Omit<Toast, 'id'>) => {
      const id = idRef.current++;
      setToasts((prev) => [...prev.slice(-2), { ...t, id }]);
      setTimeout(() => dismiss(id), 6000);
    },
    [dismiss],
  );

  return (
    <Ctx.Provider value={{ push, dismiss }}>
      {children}
      <div
        aria-live="polite"
        aria-atomic="false"
        className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-80 flex-col gap-2"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            role="status"
            className={`pointer-events-auto rounded-2xl border-l-4 bg-navy-950 p-4 text-white shadow-2xl ${
              t.tone === 'alert'
                ? 'border-red-500'
                : t.tone === 'success'
                  ? 'border-emerald-500'
                  : 'border-accent-500'
            }`}
          >
            <div className="flex items-start justify-between gap-2">
              <div className="text-sm font-bold">{t.title}</div>
              <button
                type="button"
                onClick={() => dismiss(t.id)}
                aria-label="Dismiss notification"
                className="text-slate-300 hover:text-white focus-visible:ring-2 focus-visible:ring-accent-500 rounded p-0.5"
              >
                <span aria-hidden="true">✕</span>
              </button>
            </div>
            {t.body ? <div className="mt-0.5 text-xs text-slate-200">{t.body}</div> : null}
            {t.href ? (
              <Link
                href={t.href}
                onClick={() => dismiss(t.id)}
                className="mt-2 inline-block text-xs font-semibold text-accent-300 hover:text-accent-100 underline focus-visible:ring-2 focus-visible:ring-accent-500 rounded"
              >
                {t.action ?? 'Open →'}
              </Link>
            ) : null}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}