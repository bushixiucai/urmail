// web/src/features/webmail-next/lib/format-list-date.ts

export interface ListDateLabels {
  /** Localized "Yesterday" label (e.g. 昨天 / Yesterday). */
  yesterday: string;
}

/**
 * Spark-style list timestamp gradient (local calendar days, not rolling
 * 24-hour windows):
 *   today      → HH:mm (24h, zero padded)
 *   yesterday  → localized "Yesterday"
 *   this year  → M月D日 (locale-aware, e.g. "11月8日" / "Nov 8")
 *   other year → YYYY年M月D日 (e.g. "2024年11月8日" / "Nov 8, 2024")
 *   missing    → em dash
 *
 * The locale follows the interface language, not the browser default.
 */
export function formatListDate(
  date: string | undefined,
  locale: string | undefined,
  labels: ListDateLabels,
  now: Date = new Date(),
): string {
  if (!date) return "—";

  const parsed = new Date(date);
  if (Number.isNaN(parsed.getTime())) return "—";

  const startOfDay = (d: Date) =>
    new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const dayDiff = Math.round(
    (startOfDay(now) - startOfDay(parsed)) / 86_400_000,
  );

  try {
    if (dayDiff === 0) {
      // 24-hour clock, independent of the locale's default hour cycle.
      return new Intl.DateTimeFormat(locale, {
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
        hourCycle: "h23",
      }).format(parsed);
    }
    if (dayDiff === 1) {
      return labels.yesterday;
    }
    if (parsed.getFullYear() === now.getFullYear()) {
      return new Intl.DateTimeFormat(locale, {
        month: "short",
        day: "numeric",
      }).format(parsed);
    }
    return new Intl.DateTimeFormat(locale, {
      year: "numeric",
      month: "short",
      day: "numeric",
    }).format(parsed);
  } catch {
    // Unknown locale → locale-independent numeric fallback.
    const pad = (n: number) => String(n).padStart(2, "0");
    if (dayDiff === 0) {
      return `${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
    }
    if (dayDiff === 1) return labels.yesterday;
    const md = `${parsed.getMonth() + 1}/${parsed.getDate()}`;
    return parsed.getFullYear() === now.getFullYear()
      ? md
      : `${parsed.getFullYear()}/${md}`;
  }
}

/**
 * Full local date-time used for the hover tooltip and the accessible name
 * (e.g. "2024年11月8日 09:30" / "Nov 8, 2024, 09:30"). The visible label
 * stays short so hovering never changes the row width.
 */
export function formatFullDate(
  date: string | undefined,
  locale: string | undefined,
): string {
  if (!date) return "—";
  const parsed = new Date(date);
  if (Number.isNaN(parsed.getTime())) return "—";
  try {
    return new Intl.DateTimeFormat(locale, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
      hourCycle: "h23",
    }).format(parsed);
  } catch {
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${parsed.getFullYear()}/${parsed.getMonth() + 1}/${parsed.getDate()} ${pad(
      parsed.getHours(),
    )}:${pad(parsed.getMinutes())}`;
  }
}
