function wallParts(date: Date, zone: string) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  const get = (type: string) =>
    Number(parts.find((part) => part.type === type)?.value);
  return [
    get("year"),
    get("month"),
    get("day"),
    get("hour"),
    get("minute"),
    get("second"),
  ];
}
function wallNumber(parts: number[]) {
  return Date.UTC(
    parts[0],
    parts[1] - 1,
    parts[2],
    parts[3],
    parts[4],
    parts[5] ?? 0,
  );
}
export function formatZonedInput(
  instant: string,
  zone: string,
  dateOnly = false,
) {
  const p = wallParts(new Date(instant), zone).map((n, index) =>
    String(n).padStart(index === 0 ? 4 : 2, "0"),
  );
  return `${p[0]}-${p[1]}-${p[2]}${dateOnly ? "" : `T${p[3]}:${p[4]}`}`;
}

// Resolve the user's wall clock in the selected IANA zone. Gaps are refused;
// repeated times use the explicitly selected earlier/later occurrence.
export function zonedInputToISO(
  value: string,
  zone: string,
  disambiguation: "earlier" | "later" = "earlier",
) {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2}))?$/.exec(value);
  if (!match) throw new Error("INVALID_LOCAL_TIME");
  const requested = match.slice(1).map((v) => Number(v ?? 0));
  requested.push(0);
  const wall = wallNumber(requested);
  if (
    wallParts(new Date(wall), "UTC").some(
      (value, index) => value !== requested[index],
    )
  )
    throw new Error("INVALID_LOCAL_TIME");
  const candidates = new Set<number>();
  for (const shift of [-86400000, 0, 86400000]) {
    const sample = new Date(wall + shift);
    const offset = wallNumber(wallParts(sample, zone)) - sample.getTime();
    const candidate = wall - offset;
    if (wallNumber(wallParts(new Date(candidate), zone)) === wall)
      candidates.add(candidate);
  }
  const sorted = [...candidates].sort((a, b) => a - b);
  if (!sorted.length) throw new Error("INVALID_LOCAL_TIME");
  return new Date(
    disambiguation === "later" ? sorted[sorted.length - 1] : sorted[0],
  ).toISOString();
}
