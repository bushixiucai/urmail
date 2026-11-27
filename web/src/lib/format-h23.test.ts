import { describe, expect, it, vi, afterEach } from "vitest";
import { formatH23Date, formatH23DateTime, formatH23Time } from "./format-h23";

/**
 * QA P3-3 pattern (mirrors the #164 "draft saved-at clock shape" test):
 * fake clock locked on a 15-hour instant — the h23 form must show 15:xx
 * while a locale-default 12h rendering would show 3:xx PM.
 */
describe("h23 shared formatters", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("formats time as HH:MM:SS with no AM/PM marker", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 10, 27, 15, 4, 5));
    expect(formatH23Time(new Date())).toMatch(/^\d{2}:\d{2}:\d{2}$/);
    expect(formatH23Time(new Date())).not.toMatch(/AM|PM/);
    expect(formatH23Time(new Date())).toContain("15:04:05");
  });

  it("formats date as YYYY-MM-DD", () => {
    expect(formatH23Date("2026-11-27T15:04:05Z")).toMatch(
      /^\d{4}-\d{2}-\d{2}$/,
    );
  });

  it("formats date-time as YYYY-MM-DD HH:MM:SS", () => {
    const formatted = formatH23DateTime("2026-11-27T15:04:05");
    expect(formatted).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
    expect(formatted).not.toMatch(/AM|PM/);
  });

  it("accepts strings, numbers and Date objects", () => {
    const instant = new Date(2026, 10, 27, 9, 5, 1).getTime();
    expect(formatH23Time(instant)).toBe(formatH23Time(new Date(instant)));
    expect(formatH23DateTime(new Date(instant))).toBe(
      formatH23DateTime(String(new Date(instant).toISOString())),
    );
  });

  it("renders an em dash for unparseable input instead of NaN", () => {
    expect(formatH23Time("not-a-date")).toBe("—");
    expect(formatH23Date("not-a-date")).toBe("—");
    expect(formatH23DateTime("not-a-date")).toBe("—");
  });
});
