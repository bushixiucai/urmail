import { expect, it } from "vitest";
import { usableMessageDate } from "./message-date";
it("treats a missing MIME Date and malformed date as unavailable", () => {
  for (const value of [
    "",
    "0001-01-01T00:00:00Z",
    "0001-01-01T00:00:00+00:00",
    "not-a-date",
  ])
    expect(usableMessageDate(value)).toBeNull();
  expect(usableMessageDate("2026-10-28T12:30:00Z")?.toISOString()).toBe(
    "2026-10-28T12:30:00.000Z",
  );
});
