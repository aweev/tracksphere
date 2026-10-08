'use client';

import { use, useEffect } from 'react';
import { PublicTrack } from '@/components/PublicTrack';

/**
 * Chromeless embed target for the TrackSphere widget (`/widget.js`).
 * Same PublicTrack component as /track/[n], no Shell/nav — safe to frame:
 * CSP frame-ancestors allows https framers (modern browsers prefer it over
 * X-Frame-Options). Posts its height to the parent for auto-resize; the
 * parent origin is never trusted with data (height only, one direction).
 */
export default function EmbedPage({
  params,
}: {
  params: Promise<{ trackingNumber: string }>;
}) {
  const { trackingNumber } = use(params);
  return (
    <>
      <EmbedResizer />
      <div className="mx-auto max-w-2xl p-4">
        <PublicTrack initial={trackingNumber} />
      </div>
    </>
  );
}

function EmbedResizer() {
  useEffect(() => {
    const post = () => {
      window.parent.postMessage(
        { tracksphere: 'resize', height: document.body.scrollHeight },
        '*',
      );
    };
    // Fire on mount + layout shifts (timeline/SSE updates change height).
    post();
    const t = setTimeout(post, 500);
    if (typeof ResizeObserver === 'undefined') return () => clearTimeout(t);
    const ro = new ResizeObserver(() => post());
    ro.observe(document.body);
    const stop = setTimeout(() => ro.disconnect(), 60_000);
    return () => {
      clearTimeout(t);
      clearTimeout(stop);
      ro.disconnect();
    };
  }, []);
  return null;
}
