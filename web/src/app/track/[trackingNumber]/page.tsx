'use client';

import { use } from 'react';
import { PublicTrack } from '@/components/PublicTrack';

export default function TrackByNumberPage({
  params,
}: {
  params: Promise<{ trackingNumber: string }>;
}) {
  const { trackingNumber } = use(params);
  return <PublicTrack initial={trackingNumber} />;
}
