import { describe, expect, it } from "vitest";
import { validNewPassword } from "./password-policy";

describe("new password policy", () => {
  it("counts Unicode code points and UTF-8 bytes separately", () => {
    expect(validNewPassword("a".repeat(11))).toBe(false);
    expect(validNewPassword("a".repeat(12))).toBe(true);
    expect(validNewPassword("a".repeat(256))).toBe(true);
    expect(validNewPassword("a".repeat(257))).toBe(false);
    expect(validNewPassword("密".repeat(11))).toBe(false);
    expect(validNewPassword("密".repeat(85))).toBe(true);
    expect(validNewPassword("密".repeat(86))).toBe(false);
    expect(validNewPassword("🔑".repeat(64))).toBe(true);
    expect(validNewPassword("🔑".repeat(65))).toBe(false);
    expect(validNewPassword("a".repeat(12) + "\uD800")).toBe(false);
  });

  it("does not silently trim password characters", () => {
    expect(validNewPassword(" 1234567890 ")).toBe(true);
  });
});
