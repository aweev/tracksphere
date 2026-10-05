/**
 * TrackSphere Design System — Typography Tokens
 * 
 * Font families:
 * - Plus Jakarta Sans: Primary UI, geometric humanist, excellent legibility
 * - JetBrains Mono: Technical data, tracking numbers, code, tabular nums
 * 
 * Scale: Modular scale (1.25 ratio) with optical adjustments
 * Line heights: Tight for headings, relaxed for body, mono for data
 */

export const typographyTokens = {
  /** Font families */
  fontFamily: {
    sans: ['Plus Jakarta Sans', 'ui-sans-serif', 'system-ui', 'sans-serif'],
    mono: ['JetBrains Mono', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
    display: ['Plus Jakarta Sans', 'ui-sans-serif', 'system-ui', 'sans-serif'],
  },

  /** Font weights — semantic naming */
  fontWeight: {
    light: 300,
    normal: 400,
    medium: 500,
    semibold: 600,
    bold: 700,
    extrabold: 800,
  },

  /** Font sizes — modular scale (1.25) with optical correction */
  fontSize: {
    xs: 12,    // 0.75rem
    sm: 13,    // 0.8125rem
    base: 14,  // 0.875rem
    lg: 16,    // 1rem
    xl: 20,    // 1.25rem
    '2xl': 24, // 1.5rem
    '3xl': 30, // 1.875rem
    '4xl': 36, // 2.25rem
    '5xl': 48, // 3rem
    '6xl': 60, // 3.75rem
  },

  /** Line heights — context-aware */
  lineHeight: {
    none: 1,
    tight: 1.1,
    snug: 1.25,
    normal: 1.5,
    relaxed: 1.625,
    loose: 2,
    // Specialized
    heading: 1.1,
    body: 1.6,
    caption: 1.4,
    code: 1.6,
    table: 1.4,
  },

  /** Letter spacing — optical adjustments */
  letterSpacing: {
    tighter: '-0.02em',
    tight: '-0.01em',
    normal: '0',
    wide: '0.01em',
    wider: '0.02em',
    widest: '0.04em',
    caps: '0.08em', // For uppercase labels
    tabular: '0.02em', // For monospace numbers
  },

  /** Semantic type styles — composed tokens */
  styles: {
    // Display / Marketing
    display: {
      large: { size: 60, weight: 800, lineHeight: 1.1, letterSpacing: '-0.02em' },
      medium: { size: 48, weight: 800, lineHeight: 1.1, letterSpacing: '-0.02em' },
      small: { size: 36, weight: 700, lineHeight: 1.15, letterSpacing: '-0.01em' },
    },
    // Headings
    heading: {
      h1: { size: 30, weight: 700, lineHeight: 1.15, letterSpacing: '-0.01em' },
      h2: { size: 24, weight: 700, lineHeight: 1.2, letterSpacing: '-0.01em' },
      h3: { size: 20, weight: 600, lineHeight: 1.25, letterSpacing: '0' },
      h4: { size: 16, weight: 600, lineHeight: 1.3, letterSpacing: '0' },
      h5: { size: 14, weight: 600, lineHeight: 1.4, letterSpacing: '0' },
      h6: { size: 13, weight: 600, lineHeight: 1.4, letterSpacing: '0' },
    },
    // Body text
    body: {
      large: { size: 16, weight: 400, lineHeight: 1.6, letterSpacing: '0' },
      base: { size: 14, weight: 400, lineHeight: 1.6, letterSpacing: '0' },
      small: { size: 13, weight: 400, lineHeight: 1.5, letterSpacing: '0' },
      xsmall: { size: 12, weight: 400, lineHeight: 1.5, letterSpacing: '0' },
    },
    // UI Labels & Captions
    label: {
      large: { size: 14, weight: 500, lineHeight: 1.5, letterSpacing: '0' },
      base: { size: 13, weight: 500, lineHeight: 1.5, letterSpacing: '0' },
      small: { size: 12, weight: 500, lineHeight: 1.4, letterSpacing: '0.01em' },
      xsmall: { size: 11, weight: 500, lineHeight: 1.4, letterSpacing: '0.02em' },
    },
    // Data / Mono
    data: {
      large: { size: 20, weight: 700, lineHeight: 1.3, letterSpacing: '0.01em', family: 'mono' },
      base: { size: 14, weight: 500, lineHeight: 1.5, letterSpacing: '0.01em', family: 'mono' },
      small: { size: 12, weight: 500, lineHeight: 1.5, letterSpacing: '0.02em', family: 'mono' },
      micro: { size: 11, weight: 400, lineHeight: 1.5, letterSpacing: '0.02em', family: 'mono' },
    },
    // Specialized UI
    button: { size: 13, weight: 600, lineHeight: 1.4, letterSpacing: '0.01em' },
    badge: { size: 10, weight: 700, lineHeight: 1.3, letterSpacing: '0.04em' },
    pill: { size: 11, weight: 600, lineHeight: 1.3, letterSpacing: '0.02em' },
    tooltip: { size: 12, weight: 400, lineHeight: 1.4, letterSpacing: '0' },
    toast: { size: 13, weight: 500, lineHeight: 1.5, letterSpacing: '0' },
    nav: { size: 13, weight: 500, lineHeight: 1.5, letterSpacing: '0' },
    table: { header: { size: 11, weight: 600, lineHeight: 1.4, letterSpacing: '0.04em' }, cell: { size: 13, weight: 400, lineHeight: 1.5, letterSpacing: '0' } },
  },

  /** Text decoration */
  textDecoration: {
    none: 'none',
    underline: 'underline',
    lineThrough: 'line-through',
  },

  /** Text transform */
  textTransform: {
    none: 'none',
    uppercase: 'uppercase',
    lowercase: 'lowercase',
    capitalize: 'capitalize',
  },

  /** Text overflow */
  textOverflow: {
    clip: 'clip',
    ellipsis: 'ellipsis',
  },
} as const;

export type TypographyTokens = typeof typographyTokens;
export type FontFamilyKey = keyof typeof typographyTokens.fontFamily;
export type FontWeightKey = keyof typeof typographyTokens.fontWeight;
export type FontSizeKey = keyof typeof typographyTokens.fontSize;
export type StyleKey = keyof typeof typographyTokens.styles;