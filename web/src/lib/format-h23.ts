/**
 * Locale-independent h23 (24-hour) formatters (REVIEW-UIUX-014 / QA P3-3).
 *
 * Every wall-clock timestamp rendered anywhere in the app goes through one
 * of these — bare `toLocaleTimeString()`/`toLocaleDateString()`/
 * `toLocaleString()` follow the visitor's locale and can render 12-hour
 * ("2:42:28 PM") side by side with the list pane's 24-hour clock. The h23
 * options here mirror `webmail-next/lib/format-list-date.ts`; the date
 * parts are composed manually so the shape is fully deterministic
 * (Intl numeric dates keep the locale's separator order).
 */

export type H23Input = string | number | Date;

function toParsed(value: H23Input): Date | null {
  const parsed = value instanceof Date ? value : new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** "HH:MM:SS" — returns "—" for unparseable input (render-safe). */
export function formatH23Time(value: H23Input): string {
  const parsed = toParsed(value);
  if (!parsed) return "—";
  return `${pad(parsed.getHours())}:${pad(parsed.getMinutes())}:${pad(parsed.getSeconds())}`;
}

/** "YYYY-MM-DD" — returns "—" for unparseable input (render-safe). */
export function formatH23Date(value: H23Input): string {
  const parsed = toParsed(value);
  if (!parsed) return "—";
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}`;
}

/** "YYYY-MM-DD HH:MM:SS" — returns "—" for unparseable input. */
export function formatH23DateTime(value: H23Input): string {
  const parsed = toParsed(value);
  if (!parsed) return "—";
  return `${formatH23Date(parsed)} ${formatH23Time(parsed)}`;
}

/** "HH:MM" — returns "—" for unparseable input (render-safe). */
export function formatH23TimeShort(value: H23Input): string {
  const parsed = toParsed(value);
  if (!parsed) return "—";
  return `${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
}
