import { describe, expect, it } from "vitest";
import { formatBytes, formatMoney } from "@/lib/utils";
import { ApiRequestError } from "@/lib/api/client";

describe("formatBytes", () => {
  it("formats zero and negative values", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(-1)).toBe("0 B");
  });

  it("formats byte counts with the right unit", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(1024 ** 3)).toBe("1.0 GB");
    expect(formatBytes(1024 ** 4)).toBe("1.0 TB");
  });
});

describe("formatMoney", () => {
  it("treats the amount as minor units", () => {
    expect(formatMoney(1999, "CNY")).toContain("19.99");
  });
});

describe("ApiRequestError", () => {
  it("carries the machine-readable code and the parameters", () => {
    const error = new ApiRequestError(403, {
      error: {
        code: "MAILBOX_QUOTA_EXCEEDED",
        message: "mailbox.quota_exceeded",
        params: { limit: 1024 },
      },
    });

    expect(error.status).toBe(403);
    expect(error.code).toBe("MAILBOX_QUOTA_EXCEEDED");
    expect(error.params).toEqual({ limit: 1024 });
  });
});
