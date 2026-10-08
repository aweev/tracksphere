'use client';

import { useEffect, useRef } from 'react';
// maplibre-gl v6 is pure ESM and has NO default export (verified against
// dist/maplibre-gl.mjs). Turbopack statically checks exports, so
// `import maplibregl from 'maplibre-gl'` fails the build outright. Named
// imports are the supported form.
import { LngLatBounds, Map as MLMap, Marker, Popup } from 'maplibre-gl';
import 'maplibre-gl/dist/maplibre-gl.css';
import type { Shipment } from '@/lib/api';

/**
 * Live fleet map: every active shipment with a known position, coloured by risk
 * tier rather than status.
 *
 * Colouring by status was a design mistake this fixes: "where is everything and
 * what is wrong" is the question, and a shipment 20% over its transit looks
 * identical to one on plan if both are `in_transit`. Risk tier is the answer.
 *
 * Markers are individual (no clustering yet): past ~200 points this becomes
 * a pile — the dashboard caps the query at 250 and a cluster layer is the
 * next step when a tenant outgrows it.
 */
const TIER_COLORS: Record<string, string> = {
  clear: '#10b981',
  watch: '#f59e0b',
  at_risk: '#f97316',
  critical: '#ef4444',
};

export default function FleetMap({ shipments }: { shipments: Shipment[] }) {
  const elRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MLMap | null>(null);
  const markersRef = useRef<Marker[]>([]);

  const placed = shipments.filter(
    (s) => typeof s.lat === 'number' && typeof s.lng === 'number',
  );

  useEffect(() => {
    if (!elRef.current || mapRef.current) return;
    const map = new MLMap({
      container: elRef.current,
      style: {
        version: 8,
        sources: {
          // CARTO light basemap (free with attribution, no key): OSM's
          // tile.openstreetmap.org usage policy blocks production traffic,
          // so ship CARTO by default. Tile URL override via
          // NEXT_PUBLIC_TILE_URL / NEXT_PUBLIC_TILE_ATTRIBUTION for
          // self-hosted or commercial tiles.
          carto: {
            type: 'raster',
            tiles: [
              process.env.NEXT_PUBLIC_TILE_URL ??
                'https://a.basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png',
            ],
            tileSize: 256,
            attribution:
              process.env.NEXT_PUBLIC_TILE_ATTRIBUTION ??
              '© OpenStreetMap contributors © CARTO',
          },
        },
        layers: [{ id: 'carto', type: 'raster', source: 'carto' }],
      },
      // Atlantic-centred: the lanes this product serves run between West
      // Africa and Europe.
      center: [5, 15],
      zoom: 2,
    });
    mapRef.current = map;
    return () => {
      map.remove();
      mapRef.current = null;
      markersRef.current = [];
    };
  }, []);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    markersRef.current.forEach((m) => m.remove());
    markersRef.current = [];

    const bounds = new LngLatBounds();
    for (const s of placed) {
      const tier = s.riskTier ?? 'clear';
      const el = document.createElement('div');
      el.style.width = '12px';
      el.style.height = '12px';
      el.style.borderRadius = '9999px';
      el.style.background = TIER_COLORS[tier] ?? TIER_COLORS.clear;
      el.style.border = '2px solid #ffffff';
      el.style.boxShadow = '0 1px 3px rgba(0,0,0,0.4)';
      const marker = new Marker({ element: el })
        .setLngLat([s.lng as number, s.lat as number])
        .setPopup(
          new Popup({ offset: 14 }).setHTML(
            `<strong>${escapeHtml(s.trackingNumber)}</strong><br/>` +
              `${escapeHtml(s.origin)} → ${escapeHtml(s.destination ?? '')}<br/>` +
              `<span style="opacity:.7">${escapeHtml(s.status.replace('_', ' '))} · risk ${s.riskScore ?? 0}</span>`,
          ),
        )
        .addTo(map);
      markersRef.current.push(marker);
      bounds.extend([s.lng as number, s.lat as number]);
    }

    if (placed.length > 0) {
      try {
        const pad = { top: 40, bottom: 40, left: 48, right: 48 };
        // Reduced motion: no fly. The camera still has to frame the fleet, so this
        // is a jump rather than a flight, not a skipped fit.
        if (placed.length === 1) {
          if (reduced) map.jumpTo({ center: bounds.getCenter(), zoom: 4 });
          else map.flyTo({ center: bounds.getCenter(), zoom: 4, duration: 600 });
        } else if (reduced) map.jumpTo({ center: bounds.getCenter(), zoom: 2 });
        else map.fitBounds(bounds, { padding: pad, maxZoom: 6, duration: 600 });
      } catch {
        // bounds empty (all points identical) — keep default Atlantic view.
      }
    }
  }, [placed]);

  if (placed.length === 0) {
    return (
      <div className="flex h-64 items-center justify-center rounded-2xl bg-slate-50 text-sm text-slate-400">
        No shipment positions yet — carriers report them as scans arrive.
      </div>
    );
  }

  return (
    <div className="relative">
      <div
        ref={elRef}
        className="h-64 w-full overflow-hidden rounded-2xl"
        role="img"
        aria-label={`Live fleet map showing ${placed.length} shipments, coloured by risk`}
      />
      <div className="absolute left-3 top-3 rounded-xl bg-white/95 px-3 py-2 text-[11px] shadow">
        <div className="font-semibold text-slate-700">Risk</div>
        <div className="mt-1 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full bg-emerald-500" /> On track
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full bg-amber-500" /> Watch
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full bg-orange-500" /> At risk
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full bg-red-500" /> Critical
        </div>
      </div>
    </div>
  );
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}