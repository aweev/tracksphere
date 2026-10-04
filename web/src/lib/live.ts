'use client';

import { useEffect, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useToasts } from '@/components/Toasts';

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

    const open = () => {
      const es = new EventSource('/api/v1/stream');
      ref.current = es;

      const invalidate = () => {
        void qc.invalidateQueries({ queryKey: ['shipments'] });
        void qc.invalidateQueries({ queryKey: ['dashboard'] });
        void qc.invalidateQueries({ queryKey: ['alerts'] });
        void qc.invalidateQueries({ queryKey: ['notifications'] });
      };

      es.addEventListener('shipment.updated', (e) => {
        const data = parse((e as MessageEvent).data);
        invalidate();
        if (data?.shipmentId) {
          void qc.invalidateQueries({ queryKey: ['shipment', data.shipmentId] });
        }
        pushRef.current({
          title: 'Shipment updated',
          body: 'Carrier data arrived — views refreshed.',
          tone: 'info',
          href: data?.shipmentId ? `/shipments/${data.shipmentId}` : '/shipments',
          action: 'Open shipment →',
        });
      });
      es.addEventListener('alert.changed', () => {
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
        // EventSource retries automatically unless closed.
        if (es.readyState === EventSource.CLOSED && !closed) {
          setTimeout(open, 2000);
        }
      };
    };

    open();
    return () => {
      closed = true;
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
