import { describe, expect, it } from "vitest";
import {
  isValidThemeValue,
  sanitizeTheme,
} from "@/components/brand/theme-injector";

describe("sanitizeTheme", () => {
  it("keeps whitelisted keys with valid hex colors", () => {
    expect(sanitizeTheme({ primary: "#1a2b3c", background: "#FFF" })).toEqual({
      primary: "#1a2b3c",
      background: "#FFF",
    });
  });

  it("keeps valid radius values", () => {
    expect(sanitizeTheme({ radius: "0.5rem" })).toEqual({ radius: "0.5rem" });
  });

  it("drops unknown keys", () => {
    expect(sanitizeTheme({ evil: "url(javascript:alert(1))" })).toEqual({});
  });

  it("drops malformed colors and overlong values", () => {
    expect(sanitizeTheme({ primary: "not-a-color" })).toEqual({});
    expect(sanitizeTheme({ primary: "#" + "a".repeat(40) })).toEqual({});
    expect(sanitizeTheme({ radius: "calc(1px + 2px)" })).toEqual({});
  });

  it("accepts a limited set of named colors", () => {
    expect(sanitizeTheme({ accent: "white" })).toEqual({ accent: "white" });
    expect(sanitizeTheme({ accent: "expression(alert(1))" })).toEqual({});
  });
});

describe("isValidThemeValue", () => {
  it("rejects non-string values", () => {
    expect(isValidThemeValue("primary", 123)).toBe(false);
    expect(isValidThemeValue("primary", undefined)).toBe(false);
  });

  it("accepts 3-8 digit hex colors", () => {
    expect(isValidThemeValue("primary", "#abc")).toBe(true);
    expect(isValidThemeValue("primary", "#aabbccdd")).toBe(true);
    expect(isValidThemeValue("primary", "#ab")).toBe(false);
    expect(isValidThemeValue("primary", "#aabbccdde")).toBe(false);
  });
});
