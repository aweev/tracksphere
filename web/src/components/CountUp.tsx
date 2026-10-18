'use client';

import { useEffect, useRef, useState } from 'react';

/**
 * Number that MORPHS on change (Sprint A: animate only what changed).
 *
 * A silent jump 58 → 71 reads as a new page render (change blindness);
 * a 250ms count-up makes the eye land on the delta — the score moved, and
 * it moved by this much. First paint is instant (no vanity roll-up from 0);
 * reduced motion renders the target immediately. 250ms ease-out matches the
 * risk badge color morph so color and number finish together.
 */
export function CountUp({ value }: { value: number }) {
  const [display, setDisplay] = useState(value);
  const prevRef = useRef(value);
  const rafRef = useRef<number>(0);

  useEffect(() => {
    const prev = prevRef.current;
    prevRef.current = value;
    if (prev === value) return;
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      setDisplay(value);
      return;
    }
    const start = performance.now();
    const from = display;
    const step = (t: number) => {
      // 250ms ease-out, same duration the marker ease and color morph use.
      const p = Math.min(1, (t - start) / 250);
      const eased = 1 - (1 - p) ** 3;
      setDisplay(Math.round(from + (value - from) * eased));
      if (p < 1) rafRef.current = requestAnimationFrame(step);
      else setDisplay(value);
    };
    rafRef.current = requestAnimationFrame(step);
    return () => cancelAnimationFrame(rafRef.current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  return <>{display}</>;
}
