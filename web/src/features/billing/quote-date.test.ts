import { afterEach, describe, expect, it, vi } from "vitest";
import { formatQuoteDate } from "./quote-date";

afterEach(() => vi.restoreAllMocks());
describe("quote dates use the signed sales timezone", () => {
  it("shows Shanghai January 31 even when the UTC instant is January 30", () => {
    const value = formatQuoteDate(
      "2027-01-30T16:30:00Z",
      "en-GB",
      "Asia/Shanghai",
    );
    expect(value).toContain("31 Jan 2027");
    expect(value).toContain("00:30");
  });
  it("applies New York's spring DST offset to each instant", () => {
    const before = formatQuoteDate(
      "2025-03-09T06:30:00Z",
      "en-GB",
      "America/New_York",
    );
    const after = formatQuoteDate(
      "2025-03-09T07:30:00Z",
      "en-GB",
      "America/New_York",
    );
    expect(before).toContain("9 Mar 2025");
    expect(before).toContain("01:30");
    expect(after).toContain("9 Mar 2025");
    expect(after).toContain("03:30");
  });
  it("uses UTC for legacy quotes without a calendar", () => {
    const value = formatQuoteDate("2027-01-30T16:30:00Z", "en-GB");
    expect(value).toContain("30 Jan 2027");
    expect(value).toContain("16:30");
  });
  for (const raw of ["not-a-date", "2027-99-99T25:00:00Z", ""]) {
    it(`preserves invalid date ${JSON.stringify(raw)}`, () => {
      expect(formatQuoteDate(raw, "en", "Asia/Shanghai")).toBe(raw);
    });
  }
  it("preserves the original instant if a timezone or locale is unsupported", () => {
    const raw = "2027-01-30T16:30:00Z";
    expect(formatQuoteDate(raw, "en", "Invalid/Zone")).toBe(raw);
    expect(formatQuoteDate(raw, "en", "")).toBe(raw);
    expect(formatQuoteDate(raw, "not a locale", "UTC")).toBe(raw);
  });
  it("does not crash if Intl formatting is unavailable", () => {
    vi.spyOn(Intl, "DateTimeFormat").mockImplementation(() => {
      throw new RangeError("unsupported formatter");
    });
    expect(formatQuoteDate("2027-01-30T16:30:00Z", "en", "Asia/Shanghai")).toBe(
      "2027-01-30T16:30:00Z",
    );
  });
});
