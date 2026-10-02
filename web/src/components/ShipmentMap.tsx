'use client';

import { useEffect, useRef } from 'react';
import maplibregl, { type LngLatLike, type Map as MLMap, type Marker } from 'maplibre-gl';
import 'maplibre-gl/dist/maplibre-gl.css';

export interface MapPoint {
  lng: number;
  lat: number;
  label: string;
  kind: 'event' | 'destination' | 'origin';
}

/**
 * Shipment map: OSM raster tiles (no API key), event checkpoints as markers.
 * Client-only by design (loaded with next/dynamic ssr:false on detail page).
 */
export default function ShipmentMap({ points }: { points: MapPoint[] }) {
  const elRef = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MLMap | null>(null);

  useEffect(() => {
    if (!elRef.current || mapRef.current) return;
    const map = new maplibregl.Map({
      container: elRef.current,
      style: {
        version: 8,
        sources: {
          osm: {
            type: 'raster',
            tiles: ['https://tile.openstreetmap.org/{z}/{x}/{y}.png'],
            tileSize: 256,
            attribution: '© OpenStreetMap contributors',
          },
        },
        layers: [{ id: 'osm', type: 'raster', source: 'osm' }],
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

    const markers: Marker[] = [];
    const bounds = new maplibregl.LngLatBounds();
    for (const p of points) {
      const color = p.kind === 'origin' ? '#10b981' : p.kind === 'destination' ? '#ff6b00' : '#3b82f6';
      const marker = new maplibregl.Marker({ color })
        .setLngLat([p.lng, p.lat] as LngLatLike)
        .setPopup(new maplibregl.Popup({ offset: 20 }).setText(p.label))
        .addTo(map);
      markers.push(marker);
      bounds.extend([p.lng, p.lat]);
    }
    if (points.length === 1) {
      map.flyTo({ center: [points[0].lng, points[0].lat], zoom: 8 });
    } else {
      map.fitBounds(bounds, { padding: 60 });
    }
    return () => markers.forEach((m) => m.remove());
  }, [points]);

  if (points.length === 0) {
    return (
      <div className="flex h-72 items-center justify-center rounded-2xl bg-slate-100 text-sm text-slate-400">
        No geocoded checkpoints yet
      </div>
    );
  }
  return <div ref={elRef} className="h-72 w-full overflow-hidden rounded-2xl" />;
}
