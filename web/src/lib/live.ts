'use client';

import { useEffect, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';

interface StreamEvent {
  type: string;
  shipmentId?: string;
  payload?: unknown;
}

/**
 * Live updates via SSE. Invalidates the queries the control tower depends on
 * whenever the backend broadcasts a change for this tenant. Reconnects are
 * handled by the browser (EventSource auto-retry); we re-open manually if the
 * server closes cleanly.
 */
export function useLiveStream(enabled: boolean) {
  const qc = useQueryClient();
  const ref = useRef<EventSource | null>(null);

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
      };

      es.addEventListener('shipment.updated', (e) => {
        const data = parse((e as MessageEvent).data);
        invalidate();
        if (data?.shipmentId) {
          void qc.invalidateQueries({ queryKey: ['shipment', data.shipmentId] });
        }
      });
      es.addEventListener('alert.changed', () => invalidate());
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
