/** Render an instant in the signed sales timezone without changing quote facts. */
export function formatQuoteDate(
  value: string,
  locale: string,
  timezone = "UTC",
): string {
  try {
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return value;
    return new Intl.DateTimeFormat(locale, {
      dateStyle: "medium",
      timeStyle: "short",
      timeZone: timezone,
    }).format(date);
  } catch {
    return value;
  }
}
