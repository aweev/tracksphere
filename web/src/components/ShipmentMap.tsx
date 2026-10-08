'use client';

import { useEffect, useRef } from 'react';
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
    const map = mapRef.current;
    if (!map || points.length === 0) return;
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    const markers: Marker[] = [];
    const bounds = new LngLatBounds();
    const line: Array<[number, number]> = [];
    points.forEach((p, i) => {
      const derived = points.length === 1 ? 'origin' : i === 0 ? 'origin' : i === points.length - 1 ? 'destination' : p.kind;
      const color = derived === 'origin' ? '#10b981' : derived === 'destination' ? '#ff6b00' : '#3b82f6';
      const marker = new Marker({ color })
        .setLngLat([p.lng, p.lat] as LngLatLike)
        .setPopup(new Popup({ offset: 20 }).setText(p.label))
        .addTo(map);
      markers.push(marker);
      bounds.extend([p.lng, p.lat]);
      line.push([p.lng, p.lat]);
    });

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

    if (points.length === 1) {
      const c: LngLatLike = [points[0].lng, points[0].lat];
      if (reduced) map.jumpTo({ center: c, zoom: 8 });
      else map.flyTo({ center: c, zoom: 8 });
    } else {
      map.fitBounds(bounds, { padding: 60, animate: !reduced });
    }
    return () => {
      markers.forEach((m) => m.remove());
      if (map.getLayer(ROUTE_LAYER)) map.removeLayer(ROUTE_LAYER);
      if (map.getSource(ROUTE_SOURCE)) map.removeSource(ROUTE_SOURCE);
    };
  }, [points]);

  if (points.length === 0) {
    return (
      <div className="flex h-72 items-center justify-center rounded-2xl bg-slate-100 text-sm text-slate-400">
        No geocoded checkpoints yet
      </div>
    );
  }
  return (
    <div className="relative">
      <div ref={elRef} className="h-72 w-full overflow-hidden rounded-2xl" role="img" aria-label="Shipment route map" />
      <div className="absolute left-3 top-3 rounded-xl bg-white/95 px-3 py-2 text-[11px] shadow">
        <div className="flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-full bg-emerald-500" /> First checkpoint</div>
        <div className="mt-1 flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-full bg-blue-500" /> Checkpoint</div>
        <div className="mt-1 flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-full bg-accent-500" /> Latest position</div>
        <div className="mt-1 flex items-center gap-1.5"><span className="inline-block h-0 w-4 border-t-2 border-dashed border-navy-950" /> Route</div>
      </div>
    </div>
  );
}
