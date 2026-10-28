/** Go's zero time denotes a missing RFC 5322 date, never a historical date. */
export function usableMessageDate(value: string): Date | null {
  if (!value || /^0001-01-01T00:00:00(?:\.0+)?(?:Z|[+-]00:00)$/.test(value))
    return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}
