'use client';

import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useToasts } from '@/components/Toasts';
import type { ConnState } from '@/components/ConnectionDot';

interface StreamEvent {
  type: string;
  shipmentId?: string;
  payload?: unknown;
}

/**
 * Live updates via SSE. Invalidates the queries the control tower depends on
 * whenever the backend broadcasts a change for this tenant, AND surfaces a
 * toast so the human notices (the old silent-invalidate left operators
 * staring at a list that changed under them). Reconnects are handled by the
 * browser (EventSource auto-retry); we re-open manually if the server closes
 * cleanly.
 */
export function useLiveStream(enabled: boolean) {
  const qc = useQueryClient();
  const ref = useRef<EventSource | null>(null);
  const { push } = useToasts();
  const pushRef = useRef(push);
  pushRef.current = push;

  useEffect(() => {
    if (!enabled) return;
    let closed = false;
    let attempts = 0;
    // Last SSE id seen on this connection (server sends monotonic ids).
    // The browser replays it as Last-Event-ID on auto-reconnect; we keep a
    // copy so gap-refetch logic can reason about it, and flip hadError so a
    // re-open refetches anything the outage swallowed.
    let hadError = false;

    const open = () => {
      if (closed) return;
      // Don't hold a streaming connection for a hidden tab — reconnect on
      // visible. Saves battery/radio and matches SSE-as-hint (queries are
      // truth; the stream only invalidates).
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return;
      const es = new EventSource('/api/v1/stream');
      ref.current = es;
      attempts = 0;

      const invalidate = () => {
        void qc.invalidateQueries({ queryKey: ['shipments'] });
        void qc.invalidateQueries({ queryKey: ['dashboard'] });
        void qc.invalidateQueries({ queryKey: ['alerts'] });
        void qc.invalidateQueries({ queryKey: ['notifications'] });
      };

      es.addEventListener('shipment.updated', (e) => {
        trackLastId('/api/v1/stream', e as MessageEvent);
        const data = parse((e as MessageEvent).data);
        invalidate();
        if (data?.shipmentId) {
          void qc.invalidateQueries({ queryKey: ['shipment', data.shipmentId] });
        }
        // Quiet invalidate, no toast: shipment.updated fires per carrier
        // ping and toasting each one trains operators to ignore toasts.
        // alert.changed below is the interrupting signal.
      });
      es.addEventListener('alert.changed', (e) => {
        trackLastId('/api/v1/stream', e as MessageEvent);
        invalidate();
        pushRef.current({
          title: 'Exception raised',
          body: 'A rule flagged a shipment — review the queue.',
          tone: 'alert',
          href: '/exceptions',
          action: 'Open queue →',
        });
      });
      es.onerror = () => {
        hadError = true;
        // EventSource retries automatically unless closed. On clean close,
        // reconnect with capped exponential backoff + jitter (thundering
        // herd guard when the API restarts).
        if (es.readyState === EventSource.CLOSED && !closed) {
          attempts += 1;
          const backoff = Math.min(30000, 1000 * 2 ** Math.min(attempts, 5));
          const jitter = Math.random() * 1000;
          setTimeout(open, backoff + jitter);
        }
      };
      es.addEventListener('open', () => {
        attempts = 0;
        // A re-open after a drop may have missed frames between the last
        // seen id and now (Last-Event-ID resumes the stream, but any gap
        // during the outage is only closed by refetch — queries are truth).
        if (hadError) {
          hadError = false;
          invalidate();
        }
      });
    };

    open();
    const onVisibleRoot = () => {
      if (document.visibilityState === 'visible' && !closed && !ref.current) open();
    };
    document.addEventListener('visibilitychange', onVisibleRoot);
    return () => {
      closed = true;
      document.removeEventListener('visibilitychange', onVisibleRoot);
      ref.current?.close();
      ref.current = null;
    };
  }, [enabled, qc]);
}

function parse(raw: string): StreamEvent | null {
  try {
    return JSON.parse(raw) as StreamEvent;
  } catch {
    return null;
  }
}

/** Last-Event-ID ledger. The browser sends the last seen id automatically on
 *  reconnect; recording it here keeps gap reasoning explicit and gives every
 *  tracked stream the same resume semantics. */
const lastSeenIds = new Map<string, string>();

function trackLastId(url: string, e: MessageEvent) {
  const id = (e as MessageEvent).lastEventId;
  if (id) lastSeenIds.set(url, id);
}

export function lastEventId(url: string): string | undefined {
  return lastSeenIds.get(url);
}

/**
 * Tracked single-purpose stream (shipment detail, public portal). Returns
 * the connection state for ConnectionDot and refetches on reconnect so an
 * outage gap can never strand a stale snapshot. Callers subscribe to their
 * event types via onEvent; the hook owns open/backoff/visibility/resume.
 */
export function useTrackedStream(
  url: string | null,
  opts: {
    events: string[];
    onEvent: (type: string, data: StreamEvent | null) => void;
    onReconnect?: () => void;
  },
): ConnState {
  const [state, setState] = useState<ConnState>(url ? 'connecting' : 'offline');
  const optsRef = useRef(opts);
  optsRef.current = opts;

  useEffect(() => {
    if (!url) {
      setState('offline');
      return;
    }
    let closed = false;
    let attempts = 0;
    let hadError = false;
    let es: EventSource | null = null;
    setState('connecting');

    const open = () => {
      if (closed) return;
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return;
      es = new EventSource(url);
      const handle = (type: string) => (e: Event) => {
        trackLastId(url, e as MessageEvent);
        setState('live');
        optsRef.current.onEvent(type, parse((e as MessageEvent).data));
      };
      for (const t of optsRef.current.events) es.addEventListener(t, handle(t));
      es.addEventListener('open', () => {
        attempts = 0;
        if (hadError) {
          hadError = false;
          setState('live');
          optsRef.current.onReconnect?.();
        } else {
          setState('live');
        }
      });
      es.onerror = () => {
        hadError = true;
        setState('reconnecting');
        if (es && es.readyState === EventSource.CLOSED && !closed) {
          attempts += 1;
          const backoff = Math.min(30000, 1000 * 2 ** Math.min(attempts, 5));
          setTimeout(open, backoff + Math.random() * 1000);
        }
      };
    };

    open();
    const onVisible = () => {
      if (document.visibilityState === 'visible' && !closed && !es) open();
    };
    document.addEventListener('visibilitychange', onVisible);
    const onOffline = () => setState('offline');
    const onOnline = () => {
      if (!closed) {
        es?.close();
        es = null;
        open();
      }
    };
    window.addEventListener('offline', onOffline);
    window.addEventListener('online', onOnline);
    return () => {
      closed = true;
      document.removeEventListener('visibilitychange', onVisible);
      window.removeEventListener('offline', onOffline);
      window.removeEventListener('online', onOnline);
      es?.close();
      es = null;
    };
  }, [url]);

  return state;
}
