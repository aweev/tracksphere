/**
 * TrackSphere Design System — Color Tokens
 * 
 * Advanced color theory applied to logistics domain:
 * - Navy anchor: Authority, trust, depth (maritime heritage)
 * - Amber/Orange accent: Urgency, energy, visibility (safety orange lineage)
 * - Semantic risk tiers: Perceptually uniform steps, WCAG AA compliant
 * - Status colors: Distinct hues, never relying on hue alone (icons + labels)
 */

export const colorTokens = {
  /** Brand foundation — immutable across themes */
  brand: {
    navy: {
      950: '#08111f',
      900: '#0f172a',
      850: '#141f3a',
      800: '#1e293b',
      700: '#334155',
      600: '#475569',
      500: '#64748b',
      400: '#94a3b8',
      300: '#cbd5e1',
      200: '#e2e8f0',
      100: '#f1f5f9',
      50: '#f8fafc',
    },
    accent: {
      600: '#cc5500',
      500: '#ff6b00',
      400: '#ff8533',
      300: '#ffa366',
      200: '#ffc299',
      100: '#ffe0cc',
      50: '#fff0e6',
    },
  },

  /** Semantic risk tiers — perceptually uniform, WCAG AA on both backgrounds */
  risk: {
    critical: {
      light: { fg: '#b91c1c', bg: '#fef2f2', border: '#fecaca', ring: '#ef4444' },
      dark:  { fg: '#fca5a5', bg: '#7f1d1d', border: '#991b1b', ring: '#f87171' },
    },
    at_risk: {
      light: { fg: '#c2410c', bg: '#fff7ed', border: '#fed7aa', ring: '#f97316' },
      dark:  { fg: '#fdba74', bg: '#7c2d12', border: '#9a3412', ring: '#fb923c' },
    },
    watch: {
      light: { fg: '#a16207', bg: '#fefce8', border: '#fef08a', ring: '#eab308' },
      dark:  { fg: '#fde047', bg: '#713f12', border: '#854d0e', ring: '#facc15' },
    },
    clear: {
      light: { fg: '#15803d', bg: '#f0fdf4', border: '#bbf7d0', ring: '#22c55e' },
      dark:  { fg: '#86efac', bg: '#14532d', border: '#166534', ring: '#4ade80' },
    },
  },

  /** Shipment status — distinct hues with icon+label redundancy */
  status: {
    booked: {
      light: { fg: '#334155', bg: '#e2e8f0', border: '#cbd5e1' },
      dark:  { fg: '#f1f5f9', bg: '#334155', border: '#475569' },
    },
    in_transit: {
      light: { fg: '#1d4ed8', bg: '#dbeafe', border: '#93c5fd' },
      dark:  { fg: '#93c5fd', bg: '#1e3a5f', border: '#1e40af' },
    },
    at_customs: {
      light: { fg: '#b45309', bg: '#fef3c7', border: '#fde68a' },
      dark:  { fg: '#fde68a', bg: '#78350f', border: '#92400e' },
    },
    out_for_delivery: {
      light: { fg: '#7c3aed', bg: '#ede9fe', border: '#ddd6fe' },
      dark:  { fg: '#ddd6fe', bg: '#4c1d95', border: '#6d28d9' },
    },
    delivered: {
      light: { fg: '#16a34a', bg: '#dcfce7', border: '#86efac' },
      dark:  { fg: '#86efac', bg: '#14532d', border: '#166534' },
    },
    exception: {
      light: { fg: '#dc2626', bg: '#fee2e2', border: '#fecaca' },
      dark:  { fg: '#fca5a5', bg: '#7f1d1d', border: '#991b1b' },
    },
    cancelled: {
      light: { fg: '#475569', bg: '#e2e8f0', border: '#cbd5e1' },
      dark:  { fg: '#94a3b8', bg: '#334155', border: '#475569' },
    },
  },

  /** Semantic text — hierarchical, accessible */
  text: {
    primary:   { light: '#0f172a', dark: '#f8fafc' },
    secondary: { light: '#334155', dark: '#e2e8f0' },
    tertiary:  { light: '#475569', dark: '#94a3b8' },
    disabled:  { light: '#94a3b8', dark: '#64748b' },
    inverse:   { light: '#ffffff', dark: '#0f172a' },
    link:      { light: '#ff6b00', dark: '#ff8533' },
    linkHover: { light: '#cc5500', dark: '#ffa366' },
  },

  /** Surface elevation — depth through shadow + border */
  surface: {
    base:      { light: '#ffffff', dark: '#0f172a' },
    raised:    { light: '#ffffff', dark: '#1e293b' },
    overlay:   { light: '#ffffff', dark: '#1e293b' },
    sunken:    { light: '#f1f5f9', dark: '#08111f' },
    border:    { light: '#e2e8f0', dark: '#334155' },
    borderStrong: { light: '#cbd5e1', dark: '#475569' },
  },

  /** Focus — visible, branded, accessible */
  focus: {
    ring:      { light: '#ff6b00', dark: '#ff8533' },
    ringOffset: { light: '#ffffff', dark: '#0f172a' },
  },

  /** Interactive states */
  interactive: {
    hover:     { light: '#f8fafc', dark: '#1e293b' },
    active:    { light: '#f1f5f9', dark: '#334155' },
    selected:  { light: '#fff7ed', dark: '#7c2d12' },
  },

  /** Data visualization — categorical, colorblind-safe (Okabe-Ito inspired) */
  chart: {
    categorical: [
      '#ff6b00', // accent
      '#009e73', // teal
      '#0072b2', // blue
      '#d55e00', // vermillion
      '#cc79a7', // reddish purple
      '#56b4e9', // sky blue
      '#f0e442', // yellow
      '#e69f00', // orange
    ],
    sequential: {
      navy: ['#f8fafc', '#e2e8f0', '#cbd5e1', '#94a3b8', '#64748b', '#475569', '#334155', '#1e293b', '#0f172a', '#08111f'],
      accent: ['#fff0e6', '#ffe0cc', '#ffc299', '#ffa366', '#ff8533', '#ff6b00', '#e66000', '#cc5500', '#b34a00', '#994000'],
      risk: ['#f0fdf4', '#dcfce7', '#bbf7d0', '#86efac', '#4ade80', '#22c55e', '#16a34a', '#15803d', '#166534', '#14532d'],
    },
    diverging: {
      riskToClear: ['#b91c1c', '#ef4444', '#f97316', '#fbbf24', '#eab308', '#84cc16', '#22c55e', '#16a34a', '#15803d'],
    },
  },

  /** Semantic feedback */
  feedback: {
    success:   { light: '#16a34a', dark: '#22c55e', bg: { light: '#f0fdf4', dark: '#14532d' } },
    warning:   { light: '#f59e0b', dark: '#fbbf24', bg: { light: '#fffbeb', dark: '#78350f' } },
    error:     { light: '#dc2626', dark: '#ef4444', bg: { light: '#fef2f2', dark: '#7f1d1d' } },
    info:      { light: '#3b82f6', dark: '#60a5fa', bg: { light: '#eff6ff', dark: '#1e3a5f' } },
  },
} as const;

export type ColorTokens = typeof colorTokens;
export type RiskTier = keyof typeof colorTokens.risk;
export type StatusKey = keyof typeof colorTokens.status;
export type TextLevel = keyof typeof colorTokens.text;
export type SurfaceLevel = keyof typeof colorTokens.surface;