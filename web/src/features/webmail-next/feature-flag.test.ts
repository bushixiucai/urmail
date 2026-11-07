import { describe, it, expect, beforeEach } from "vitest";
import { isWebmailNextEnabled, setWebmailNextEnabled } from "./feature-flag";

describe("Webmail Next Feature Flag", () => {
  beforeEach(() => {
    window.localStorage.clear();
    window.sessionStorage.clear();
    window.history.replaceState({}, "", "/");
  });

  it("defaults to true when no query, storage, or preference is set", () => {
    expect(isWebmailNextEnabled()).toBe(true);
    expect(isWebmailNextEnabled(null)).toBe(true);
  });

  it("disables when the query asks for the legacy client", () => {
    window.history.replaceState({}, "", "/webmail/inbox?v=legacy");
    expect(isWebmailNextEnabled()).toBe(false);
  });

  it("enables when localStorage flag is set to next", () => {
    setWebmailNextEnabled(true);
    expect(isWebmailNextEnabled()).toBe(true);
  });

  it("disables when setWebmailNextEnabled(false) is called", () => {
    setWebmailNextEnabled(true);
    expect(isWebmailNextEnabled()).toBe(true);
    setWebmailNextEnabled(false);
    expect(isWebmailNextEnabled()).toBe(false);
  });

  it("enables when preference has webmail_version=next", () => {
    expect(isWebmailNextEnabled({ webmail_version: "next" })).toBe(true);
  });

  it("disables when preference has webmail_version=legacy even if storage was next", () => {
    setWebmailNextEnabled(true);
    expect(isWebmailNextEnabled({ webmail_version: "legacy" })).toBe(false);
  });
});
