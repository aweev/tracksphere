'use client';

import { useEffect, useRef, useState } from 'react';
// v6 has no default export — see FleetMap.tsx for the full reason.
import { LngLatBounds, Map as MLMap, Marker, Popup, type LngLatLike } from 'maplibre-gl';
import 'maplibre-gl/dist/maplibre-gl.css';

export interface MapPoint {
  lng: number;
  lat: number;
  label: string;
  /** 'event' for checkpoints; origin/destination are derived by order. */
  kind: 'event' | 'destination' | 'origin';
}

const ROUTE_SOURCE = 'ts-route';
const ROUTE_LAYER = 'ts-route-line';

/**
 * Shipment map: OSM raster tiles (no API key), checkpoint markers, dashed
 * route polyline from first to latest checkpoint, origin/destination pins,
 * legend. Client-only (next/dynamic ssr:false on the detail page).
 */
export default function ShipmentMap({ points }: { points: MapPoint[] }) {
  const elRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MLMap | null>(null);
  // Camera follows arrivals, not renders: refit only when the checkpoint
  // COUNT changes (a new scan arrived), never on re-render. Otherwise every
  // query refetch yanks the viewport out from under a reading operator.
  const markersRef = useRef<Marker[]>([]);
  const fittedCountRef = useRef(0);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    if (!elRef.current || mapRef.current) return;
    const map = new MLMap({
      container: elRef.current,
      style: {
        version: 8,
        sources: {
          // Same compliant default as FleetMap: CARTO light, overridable
          // via NEXT_PUBLIC_TILE_URL / NEXT_PUBLIC_TILE_ATTRIBUTION.
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
      center: [8, 10],
      zoom: 1.5,
    });
    mapRef.current = map;
    return () => {
      map.remove();
      mapRef.current = null;
    };
  }, []);

  useEffect(() => {
    mapRef.current?.resize();
  }, [expanded]);

  useEffect(() => {
    const map = mapRef.current;
    if (!map || points.length === 0) return;
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    // Indexed pool: checkpoints are append-mostly, so index identity is
    // stable. A corrected position EASES (450ms) instead of jumping; a brand
    // new checkpoint drops in (ts-drop 200ms). Removed tail markers are
    // pruned. Route truth redraws fully — it is a polyline of facts, and a
    // fake "drawing" animation would imply progress we cannot measure.
    const prev = markersRef.current;
    const next: Marker[] = [];
    const bounds = new LngLatBounds();
    const line: Array<[number, number]> = [];
    points.forEach((p, i) => {
      const derived = points.length === 1 ? 'origin' : i === 0 ? 'origin' : i === points.length - 1 ? 'destination' : p.kind;
      // Journey semantics, not risk: origin/destination/checkpoint. These are
      // deliberately NOT the risk palette (a checkpoint is not a warning).
      const color = derived === 'origin' ? '#15803d' : derived === 'destination' ? '#b34a00' : '#3b82f6';
      const existing = prev[i];
      if (existing) {
        const el = existing.getElement();
        if (!reduced && !el.style.transition) {
          el.style.transition = 'transform 450ms ease-out';
        }
        existing.setLngLat([p.lng, p.lat] as LngLatLike);
        existing.setPopup(new Popup({ offset: 20 }).setText(p.label));
        next.push(existing);
      } else {
        const marker = new Marker({ color })
          .setLngLat([p.lng, p.lat] as LngLatLike)
          .setPopup(new Popup({ offset: 20 }).setText(p.label))
          .addTo(map);
        if (!reduced) marker.getElement().classList.add('ts-drop');
        next.push(marker);
      }
      bounds.extend([p.lng, p.lat]);
      line.push([p.lng, p.lat]);
    });
    for (let i = next.length; i < prev.length; i++) prev[i].remove();
    markersRef.current = next;

    const drawRoute = () => {
      if (line.length < 2) return;
      if (map.getSource(ROUTE_SOURCE)) return;
      map.addSource(ROUTE_SOURCE, {
        type: 'geojson',
        data: { type: 'Feature', properties: {}, geometry: { type: 'LineString', coordinates: line } },
      });
      map.addLayer({
        id: ROUTE_LAYER,
        type: 'line',
        source: ROUTE_SOURCE,
        paint: { 'line-color': '#0f172a', 'line-width': 2, 'line-dasharray': [2, 2], 'line-opacity': 0.7 },
      });
    };
    if (map.loaded()) {
      drawRoute();
    } else {
      map.once('load', drawRoute);
    }

    if (fittedCountRef.current !== points.length) {
      fittedCountRef.current = points.length;
      if (points.length === 1) {
        const c: LngLatLike = [points[0].lng, points[0].lat];
        if (reduced) map.jumpTo({ center: c, zoom: 8 });
        else map.flyTo({ center: c, zoom: 8 });
      } else {
        map.fitBounds(bounds, { padding: 60, animate: !reduced });
      }
    }
    return () => {
      // Markers persist across updates (pool); only the route layer is
      // transient per redraw.
      if (map.getLayer(ROUTE_LAYER)) map.removeLayer(ROUTE_LAYER);
      if (map.getSource(ROUTE_SOURCE)) map.removeSource(ROUTE_SOURCE);
    };
  }, [points]);

  if (points.length === 0) {
    return (
      <div className="flex h-72 items-center justify-center rounded-2xl bg-slate-100 text-sm text-slate-600">
        No geocoded checkpoints yet
      </div>
    );
  }
  return (
    <div className="relative">
      <div ref={elRef} className={`w-full overflow-hidden rounded-2xl ${expanded ? 'h-96' : 'h-40 md:h-72'}`} role="img" aria-label="Shipment route map" />
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        aria-label={expanded ? 'Collapse route map' : 'Expand route map'}
        className="absolute bottom-3 right-3 rounded-xl bg-white/95 px-3 py-1.5 text-xs font-semibold text-navy-950 shadow md:hidden"
      >
        {expanded ? 'Collapse' : 'Expand'}
      </button>
      <div className="absolute left-3 top-3 rounded-xl bg-white/95 px-3 py-2 text-[11px] shadow">
        <div className="flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-full bg-emerald-700" /> First checkpoint</div>
        <div className="mt-1 flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-full bg-blue-500" /> Checkpoint</div>
        <div className="mt-1 flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-full bg-accent-700" /> Latest position</div>
        <div className="mt-1 flex items-center gap-1.5"><span className="inline-block h-0 w-4 border-t-2 border-dashed border-navy-950" /> Route</div>
      </div>
    </div>
  );
}
