import { describe, expect, it } from "vitest";
import { avatarInitial } from "./avatar-initial";

describe("avatarInitial (F4 tofu fallback)", () => {
  it("takes the first letter, upper-cased", () => {
    expect(avatarInitial("alice")).toBe("A");
    expect(avatarInitial("  张三 ")).toBe("张");
    expect(avatarInitial("42 Co")).toBe("4");
  });

  it("strips RFC 2047 encoded-words before scanning", () => {
    expect(avatarInitial("=?utf-8?B?VFVUVS5UTw==?=", "tutu.to")).toBe("T");
    expect(avatarInitial("=?GB2312?Q? =?=", "noreply@example.com")).toBe("N");
  });

  it("skips unsafe glyphs that would render as tofu", () => {
    expect(avatarInitial("� Broken")).toBe("B");
    expect(avatarInitial("🎉party")).toBe("P");
    expect(avatarInitial("= ?")).toBe("@");
  });

  it("falls back to the email local part, then @", () => {
    expect(avatarInitial("", "bob@example.com")).toBe("B");
    expect(avatarInitial(null)).toBe("@");
    expect(avatarInitial("   ")).toBe("@");
  });
});
