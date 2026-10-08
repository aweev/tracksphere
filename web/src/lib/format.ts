/** Relative-time helpers shared by timeline, exceptions and detail views. */

/** "3h ago" / "in 2d" style label for an ISO timestamp. */
export function relativeTime(iso: string | undefined, nowMs = Date.now()): string {
  if (!iso) return '—';
  const diff = new Date(iso).getTime() - nowMs;
  const abs = Math.abs(diff);
  const mins = Math.round(abs / 60000);
  const fmt = (n: number, unit: string) =>
    diff >= 0 ? `in ${n}${unit}` : `${n}${unit} ago`;
  if (mins < 1) return 'just now';
  if (mins < 60) return fmt(mins, 'm');
  const hours = Math.round(mins / 60);
  if (hours < 48) return fmt(hours, 'h');
  return fmt(Math.round(hours / 24), 'd');
}


