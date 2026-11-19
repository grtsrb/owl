// Inline stroke icons (no icon font or CDN). Decorative: aria-hidden; buttons carry their own labels.
import type { ReactNode } from "react";

function Svg({ children, size = 16 }: { children: ReactNode; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{children}</svg>
  );
}

export const Icon = {
  /** Owl's logo (filled, one colour: currentColor). Eyes and beak are holes (even-odd fill). */
  logo: ({ size = 18 }: { size?: number } = {}) => (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <path fill="currentColor" fillRule="evenodd" d="M6.2 8.6 8.6 3.4l4.4 4.3q3-.9 6 0l4.4-4.3 2.4 5.2q2.4 4 1.8 10.2C26.9 25.4 22 29 16 29S5.1 25.4 4.4 18.8Q3.8 12.6 6.2 8.6zM6.6 16a4.6 4.6 0 1 0 9.2 0 4.6 4.6 0 1 0-9.2 0zm2.6 0a2 2 0 1 0 4 0 2 2 0 1 0-4 0zm7 0a4.6 4.6 0 1 0 9.2 0 4.6 4.6 0 1 0-9.2 0zm2.6 0a2 2 0 1 0 4 0 2 2 0 1 0-4 0zM14.4 21.2 16 24.4l1.6-3.2z" />
    </svg>
  ),
  overview: () => <Svg size={18}><rect x="3" y="3" width="7" height="9" rx="1.5" /><rect x="14" y="3" width="7" height="5" rx="1.5" /><rect x="14" y="12" width="7" height="9" rx="1.5" /><rect x="3" y="16" width="7" height="5" rx="1.5" /></Svg>,
  clusters: () => <Svg size={18}><rect x="3" y="4" width="18" height="7" rx="1.5" /><rect x="3" y="13" width="18" height="7" rx="1.5" /><path d="M7 7.5h.01M7 16.5h.01" /></Svg>,
  changes: () => <Svg size={18}><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></Svg>,
  settings: () => <Svg size={18}><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1" /></Svg>,
  signout: () => <Svg size={18}><path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 17l-5-5 5-5M5 12h11" /></Svg>,
  close: () => <Svg size={18}><path d="M6 6l12 12M18 6 6 18" /></Svg>,
  warn: () => <Svg><path d="M12 3 2 20h20z" /><path d="M12 10v4M12 17h.01" /></Svg>,
  info: () => <Svg><circle cx="12" cy="12" r="9" /><path d="M12 11v5M12 8h.01" /></Svg>,
  retired: () => <Svg size={14}><circle cx="12" cy="12" r="9" /><path d="m5.6 5.6 12.8 12.8" /></Svg>,
  archived: () => <Svg size={14}><rect x="3" y="4" width="18" height="5" rx="1" /><path d="M5 9v10h14V9M10 13h4" /></Svg>,
  drift: () => <Svg size={14}><path d="M7 4 3 8l4 4M3 8h13M17 20l4-4-4-4M21 16H8" /></Svg>,
  pin: () => <Svg size={14}><path d="M12 17v5M9 3h6l-1 6 3 3v2H7v-2l3-3z" /></Svg>,
  review: () => <Svg size={14}><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z" /><circle cx="12" cy="12" r="3" /></Svg>,
  nocve: () => <Svg size={14}><path d="M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6z" /><path d="M10 10a2 2 0 1 1 2.8 1.8c-.5.3-.8.7-.8 1.2M12 16h.01" /></Svg>,
  stale: () => <Svg size={14}><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></Svg>,
};
