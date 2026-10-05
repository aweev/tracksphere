/**
 * TrackSphere Design System — Shadow & Elevation Tokens
 * 
 * Elevation system based on material metaphors adapted for data-dense interfaces.
 * Shadows communicate hierarchy, not decoration.
 */

export const shadowTokens = {
  /** Elevation levels — each has a distinct shadow + surface treatment */
  elevation: {
    0: {
      boxShadow: 'none',
      border: '1px solid var(--color-border-subtle)',
    },
    1: {
      boxShadow: '0 1px 2px 0 rgb(0 0 0 / 0.05)',
      border: '1px solid var(--color-border-subtle)',
    },
    2: {
      boxShadow: '0 1px 3px 0 rgb(0 0 0 / 0.1), 0 1px 2px -1px rgb(0 0 0 / 0.1)',
      border: '1px solid var(--color-border-subtle)',
    },
    3: {
      boxShadow: '0 4px 6px -1px rgb(0 0 0 / 0.1), 0 2px 4px -2px rgb(0 0 0 / 0.1)',
      border: '1px solid var(--color-border-subtle)',
    },
    4: {
      boxShadow: '0 10px 15px -3px rgb(0 0 0 / 0.1), 0 4px 6px -4px rgb(0 0 0 / 0.1)',
      border: '1px solid var(--color-border-subtle)',
    },
    5: {
      boxShadow: '0 20px 25px -5px rgb(0 0 0 / 0.1), 0 8px 10px -6px rgb(0 0 0 / 0.1)',
      border: '1px solid var(--color-border-subtle)',
    },
  },

  /** Semantic shadow presets */
  preset: {
    card: 'elevation-1',
    cardHover: 'elevation-2',
    cardPressed: 'elevation-1',
    dropdown: 'elevation-3',
    modal: 'elevation-5',
    popover: 'elevation-3',
    tooltip: 'elevation-2',
    toast: 'elevation-4',
    commandPalette: 'elevation-5',
    sidebar: 'elevation-2',
    header: 'elevation-1',
    fab: 'elevation-3',
    fabHover: 'elevation-4',
  },

  /** Colored shadows for brand moments */
  colored: {
    accent: '0 4px 14px 0 rgb(255 107 0 / 0.3)',
    accentHover: '0 6px 20px 0 rgb(255 107 0 / 0.4)',
    critical: '0 4px 14px 0 rgb(220 38 38 / 0.3)',
    success: '0 4px 14px 0 rgb(34 197 94 / 0.3)',
  },

  /** Inner shadows for sunken states */
  inset: {
    sm: 'inset 0 1px 2px 0 rgb(0 0 0 / 0.05)',
    md: 'inset 0 2px 4px 0 rgb(0 0 0 / 0.06)',
    lg: 'inset 0 4px 8px 0 rgb(0 0 0 / 0.08)',
    input: 'inset 0 1px 2px 0 rgb(0 0 0 / 0.05)',
    inputFocus: 'inset 0 1px 2px 0 rgb(0 0 0 / 0.05), 0 0 0 2px var(--color-focus-ring)',
  },
} as const;

export type ShadowTokens = typeof shadowTokens;
export type ElevationKey = keyof typeof shadowTokens.elevation;
export type ShadowPresetKey = keyof typeof shadowTokens.preset;