'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
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
// MapLibre paint properties cannot resolve CSS var(); these hexes are kept
// identical to the @theme --color-risk-* tokens in globals.css BY HAND.
// If you change one, change the other — risk must mean the same on the map
// as in the queue (habit formation depends on it).
const TIER_COLORS: Record<string, string> = {
  clear: '#15803d', // --color-risk-clear
  watch: '#a16207', // --color-risk-watch
  at_risk: '#c2410c', // --color-risk-at-risk
  critical: '#b91c1c', // --color-risk-critical
};

/** Dwell distress: dwell past the lane norm (ratio > 1) with both facts known. */
function dwellLabel(s: Shipment): string | null {
  if (
    typeof s.dwellHours !== 'number' ||
    typeof s.expectedDwellHours !== 'number' ||
    s.expectedDwellHours <= 0 ||
    (s.dwellRatio ?? 0) <= 1
  ) {
    return null;
  }
  const days = (h: number) => {
    if (h < 48) return `${Math.round(h)}h`;
    return `${(h / 24).toFixed(h < 240 ? 1 : 0)}d`;
  };
  return `${days(s.dwellHours)} vs ${days(s.expectedDwellHours)} norm`;
}

interface PooledMarker {
  marker: Marker;
  dot: HTMLDivElement;
}

export default function FleetMap({
  shipments,
  disruptedIds,
}: {
  shipments: Shipment[];
  /** Shipment ids with an open disruption-kind alert: honest overlay data. */
  disruptedIds?: string[];
}) {
  const elRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MLMap | null>(null);
  const poolRef = useRef(new Map<string, PooledMarker>());
  const fittedRef = useRef(false);
  const [expanded, setExpanded] = useState(false);
  const disrupted = useMemo(() => new Set(disruptedIds ?? []), [disruptedIds]);
  const disruptedKey = (disruptedIds ?? []).join(',');

  const placed = useMemo(
    () =>
      shipments.filter(
        (s) => typeof s.lat === 'number' && typeof s.lng === 'number',
      ),
    [shipments],
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
      poolRef.current.clear();
    };
  }, []);

  // The mobile strip changes the container height — tell MapLibre to resize
  // or tiles render at the old size and the map looks broken.
  useEffect(() => {
    mapRef.current?.resize();
  }, [expanded]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const pool = poolRef.current;

    // Marker POOL keyed by shipment id (change-blindness guard): the old code
    // destroyed and recreated every marker on every data refresh, so dots
    // jumped, popups died, and the camera re-flew. Now only what changed
    // moves — a dot whose position updated EASES there (450ms), everything
    // else sits still. Removed shipments are pruned; new ones appear.
    const seen = new Set<string>();
    const bounds = new LngLatBounds();
    for (const s of placed) {
      seen.add(s.id);
      const tier = s.riskTier ?? 'clear';
      const color = TIER_COLORS[tier] ?? TIER_COLORS.clear;
      const dwell = dwellLabel(s);
      const isDisrupted = disrupted.has(s.id);
      let entry = pool.get(s.id);
      if (!entry) {
        // Halo wrapper: distress ring lives OUTSIDE the dot so the dot's
        // own transform (maplibre positions it) is never fought by CSS.
        const wrap = document.createElement('div');
        wrap.style.position = 'relative';
        wrap.style.width = '12px';
        wrap.style.height = '12px';
        const dot = document.createElement('div');
        dot.style.width = '12px';
        dot.style.height = '12px';
        dot.style.borderRadius = '9999px';
        dot.style.border = '2px solid #ffffff';
        dot.style.boxShadow = '0 1px 3px rgba(0,0,0,0.4)';
        if (!reduced) dot.style.transition = 'background-color 250ms ease-out';
        wrap.appendChild(dot);
        const marker = new Marker({ element: wrap })
          .setLngLat([s.lng as number, s.lat as number])
          .addTo(map);
        entry = { marker, dot };
        pool.set(s.id, entry);
      }
      // Travel: maplibre rewrites the wrapper transform every frame; a CSS
      // transition on the WRAPPER eases the dot to its new position instead
      // of jumping. Set once per element — cheap, idempotent.
      const wrap = entry.marker.getElement();
      if (!reduced && !wrap.style.transition) {
        wrap.style.transition = 'transform 450ms ease-out';
      }
      entry.marker.setLngLat([s.lng as number, s.lat as number]);
      entry.dot.style.background = color;
      // Distress halo: dwell past norm, or a live disruption alert on this
      // shipment. Soft and local — never red-everything. Pulsing halo only
      // while the condition holds; static ring under reduced motion.
      const haloClass = 'ts-fleet-halo';
      let halo = wrap.querySelector<HTMLElement>(`.${haloClass}`);
      const haloColor = isDisrupted ? '194,65,12' : '161,98,7';
      const haloOpacity = isDisrupted ? 0.12 : 0.35;
      if (dwell || isDisrupted) {
        if (!halo) {
          halo = document.createElement('div');
          halo.className = haloClass + (reduced ? '' : ' ts-halo');
          halo.setAttribute('aria-hidden', 'true');
          halo.style.position = 'absolute';
          halo.style.inset = '-8px';
          halo.style.borderRadius = '9999px';
          halo.style.border = `2px solid rgba(${haloColor},${haloOpacity + 0.25})`;
          halo.style.background = `rgba(${haloColor},${haloOpacity})`;
          halo.style.pointerEvents = 'none';
          wrap.appendChild(halo);
        } else {
          halo.style.border = `2px solid rgba(${haloColor},${haloOpacity + 0.25})`;
          halo.style.background = `rgba(${haloColor},${haloOpacity})`;
        }
      } else if (halo) {
        halo.remove();
      }
      entry.marker.setPopup(
        new Popup({ offset: 14 }).setHTML(
          `<strong>${escapeHtml(s.trackingNumber)}</strong><br/>` +
            `${escapeHtml(s.origin)} → ${escapeHtml(s.destination ?? '')}<br/>` +
            `<span style="opacity:.7">${escapeHtml(s.status.replace('_', ' '))} · risk ${s.riskScore ?? 0}</span>` +
            (dwell ? `<br/><span>Dwelling ${escapeHtml(dwell)}</span>` : '') +
            (isDisrupted ? `<br/><span>Disruption reported on this lane</span>` : ''),
        ),
      );
      bounds.extend([s.lng as number, s.lat as number]);
    }
    for (const [id, entry] of pool) {
      if (!seen.has(id)) {
        entry.marker.remove();
        pool.delete(id);
      }
    }

    // Frame the fleet ONCE. Re-flying on every live update yanks the camera
    // out from under a reading operator — the map is ambient, not the star.
    if (placed.length > 0 && !fittedRef.current) {
      fittedRef.current = true;
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [placed, disruptedKey]);

  if (placed.length === 0) {
    return (
      <div className="flex h-64 items-center justify-center rounded-2xl bg-slate-50 text-sm text-slate-600">
        No shipment positions yet — carriers report them as scans arrive.
      </div>
    );
  }

  return (
    <div className="relative">
      <div
        ref={elRef}
        className={`w-full overflow-hidden rounded-2xl ${expanded ? 'h-96' : 'h-40 md:h-64'}`}
        role="img"
        aria-label={`Live fleet map showing ${placed.length} shipments, coloured by risk`}
      />
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        aria-label={expanded ? 'Collapse fleet map' : 'Expand fleet map'}
        className="absolute bottom-3 right-3 rounded-xl bg-white/95 px-3 py-1.5 text-xs font-semibold text-navy-950 shadow md:hidden"
      >
        {expanded ? 'Collapse' : 'Expand'}
      </button>
      <div className="absolute left-3 top-3 rounded-xl bg-white/95 px-3 py-2 text-[11px] shadow">
        <div className="font-semibold text-slate-700">Risk</div>
        <div className="mt-1 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full" style={{ background: TIER_COLORS.clear }} /> On track
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full" style={{ background: TIER_COLORS.watch }} /> Watch
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full" style={{ background: TIER_COLORS.at_risk }} /> At risk
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          <span className="h-2.5 w-2.5 rounded-full" style={{ background: TIER_COLORS.critical }} /> Critical
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