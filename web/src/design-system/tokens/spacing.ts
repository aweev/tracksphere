/**
 * TrackSphere Design System — Spacing & Layout Tokens
 * 
 * 4px base unit with semantic scales for different contexts:
 * - Compact: High-density data tables, dashboards
 * - Comfortable: Default application spacing
 * - Spacious: Marketing, onboarding, empty states
 */

export const spacingTokens = {
  /** Base unit: 4px */
  unit: 4,

  /** Semantic spacing scale */
  space: {
    0: 0,
    1: 4,   // 0.25rem
    2: 8,   // 0.5rem
    3: 12,  // 0.75rem
    4: 16,  // 1rem
    5: 20,  // 1.25rem
    6: 24,  // 1.5rem
    7: 28,  // 1.75rem
    8: 32,  // 2rem
    10: 40, // 2.5rem
    12: 48, // 3rem
    14: 56, // 3.5rem
    16: 64, // 4rem
    20: 80, // 5rem
    24: 96, // 6rem
    28: 112, // 7rem
    32: 128, // 8rem
  },

  /** Contextual spacing presets */
  preset: {
    compact: {
      xs: 4,
      sm: 8,
      md: 12,
      lg: 16,
      xl: 24,
    },
    comfortable: {
      xs: 8,
      sm: 12,
      md: 16,
      lg: 24,
      xl: 32,
    },
    spacious: {
      xs: 12,
      sm: 16,
      md: 24,
      lg: 32,
      xl: 48,
    },
  },

  /** Component-specific spacing */
  component: {
    card: { padding: 24, gap: 16 },
    button: { paddingX: 16, paddingY: 10, gap: 8 },
    input: { paddingX: 12, paddingY: 10 },
    badge: { paddingX: 8, paddingY: 4, gap: 4 },
    pill: { paddingX: 10, paddingY: 4, gap: 6 },
    table: { cellPaddingX: 16, cellPaddingY: 12, headerPaddingY: 10 },
    modal: { padding: 24, gap: 16 },
    drawer: { padding: 20, gap: 16 },
    toast: { padding: 16, gap: 12 },
    tooltip: { padding: 8, gap: 6 },
    dropdown: { itemPaddingX: 12, itemPaddingY: 10, gap: 4 },
    tabs: { paddingX: 16, paddingY: 12, gap: 4 },
    list: { itemGap: 8, itemPadding: 12 },
    form: { fieldGap: 20, groupGap: 32 },
    page: { padding: 24, sectionGap: 32, headerGap: 16 },
  },

  /** Layout constraints */
  layout: {
    maxWidth: {
      sm: 640,
      md: 768,
      lg: 1024,
      xl: 1280,
      '2xl': 1536,
      full: '100%',
    },
    container: {
      padding: { mobile: 16, tablet: 24, desktop: 32 },
    },
    sidebar: { width: 256, collapsedWidth: 64 },
    header: { height: 64 },
    footer: { height: 80 },
  },

  /** Border radius — semantic, not arbitrary */
  radius: {
    none: 0,
    xs: 2,
    sm: 4,
    md: 8,
    lg: 12,
    xl: 16,
    '2xl': 24,
    '3xl': 32,
    full: 9999,
    pill: 9999,
  },

  /** Border width */
  borderWidth: {
    none: 0,
    thin: 1,
    medium: 2,
    thick: 3,
    focus: 2,
    focusOffset: 2,
  },

  /** Z-index scale — semantic layers */
  zIndex: {
    base: 0,
    dropdown: 100,
    sticky: 200,
    modal: 400,
    popover: 500,
    tooltip: 600,
    toast: 700,
    commandPalette: 800,
    loading: 900,
  },
} as const;

export type SpacingTokens = typeof spacingTokens;
export type SpaceKey = keyof typeof spacingTokens.space;
export type RadiusKey = keyof typeof spacingTokens.radius;
export type ZIndexKey = keyof typeof spacingTokens.zIndex;