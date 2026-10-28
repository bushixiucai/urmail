import { describe, expect, it } from "vitest";
import { formatZonedInput, zonedInputToISO } from "./people-time";

describe("calendar wall-clock input", () => {
  it("keeps the selected zone rather than the browser zone", () => {
    expect(zonedInputToISO("2026-10-28T09:30", "Asia/Shanghai")).toBe(
      "2026-10-28T01:30:00.000Z",
    );
    expect(formatZonedInput("2026-10-28T01:30:00Z", "Asia/Shanghai")).toBe(
      "2026-10-28T09:30",
    );
  });
  it("refuses nonexistent daylight-saving times", () => {
    expect(() =>
      zonedInputToISO("2026-03-08T02:30", "America/New_York"),
    ).toThrow("INVALID_LOCAL_TIME");
  });
  it("lets the user choose both sides of a repeated time", () => {
    expect(
      zonedInputToISO("2026-11-01T01:30", "America/New_York", "earlier"),
    ).toBe("2026-11-01T05:30:00.000Z");
    expect(
      zonedInputToISO("2026-11-01T01:30", "America/New_York", "later"),
    ).toBe("2026-11-01T06:30:00.000Z");
  });
});
