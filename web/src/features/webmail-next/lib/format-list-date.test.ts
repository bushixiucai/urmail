import { describe, expect, it } from "vitest";
import { formatListDate } from "./format-list-date";

const labels = { yesterday: "昨天" };
// 2026-11-08 is a Sunday; use a fixed "now" so the cases are stable.
const NOW = new Date(2026, 10, 8, 15, 30, 0);

function at(hours: number, minutes: number, day = 8, month = 10) {
  return new Date(2026, month, day, hours, minutes, 0).toISOString();
}

describe("formatListDate", () => {
  it("shows HH:mm (24h) for today", () => {
    expect(formatListDate(at(9, 5), "zh-CN", labels, NOW)).toBe("09:05");
    expect(formatListDate(at(23, 59), "zh-CN", labels, NOW)).toBe("23:59");
    expect(formatListDate(at(0, 0), "zh-CN", labels, NOW)).toBe("00:00");
  });

  it("shows the localized yesterday label", () => {
    expect(formatListDate(at(10, 0, 7), "zh-CN", labels, NOW)).toBe("昨天");
    expect(
      formatListDate(at(10, 0, 7), "en", { yesterday: "Yesterday" }, NOW),
    ).toBe("Yesterday");
  });

  it("shows month/day for earlier dates in the same year", () => {
    const out = formatListDate(at(10, 0, 3), "zh-CN", labels, NOW);
    expect(out).toContain("11");
    expect(out).toContain("3");
    expect(out).not.toContain("2026");
  });

  it("includes the year for dates in earlier years", () => {
    const out = formatListDate(
      new Date(2025, 10, 3, 10, 0).toISOString(),
      "zh-CN",
      labels,
      NOW,
    );
    expect(out).toContain("2025");
    expect(out).toContain("11");
    expect(out).toContain("3");
  });

  it("falls back to an em dash for missing or invalid dates", () => {
    expect(formatListDate(undefined, "zh-CN", labels, NOW)).toBe("—");
    expect(formatListDate("", "zh-CN", labels, NOW)).toBe("—");
    expect(formatListDate("not-a-date", "zh-CN", labels, NOW)).toBe("—");
  });
});
