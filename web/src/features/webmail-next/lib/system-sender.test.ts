import { describe, expect, it } from "vitest";
import { isSystemSender } from "./system-sender";

describe("isSystemSender (Q1 mirror of threading.go systemSenderMarkers)", () => {
  it("flags bounce daemon and postmaster addresses", () => {
    expect(
      isSystemSender("mailer-daemon@box.urmail.cn", "Mail Delivery System"),
    ).toBe(true);
    expect(isSystemSender("postmaster@example.com", "Postmaster")).toBe(true);
  });

  it("flags marker variants in either the email or the display name", () => {
    expect(isSystemSender("no-reply@cdn.example", "CDN")).toBe(true);
    // "bounces" is not a backend marker — a plain delivery-status sender
    // with a human-free name stays out of the blacklist.
    expect(isSystemSender("bounces+123@send.example", "Delivery Status")).toBe(
      false,
    );
    expect(isSystemSender("", "Mail Delivery System (noreply)")).toBe(true);
    expect(isSystemSender("donotreply@x", "")).toBe(true);
  });

  it("does not flag human senders or empty identities", () => {
    expect(isSystemSender("ada@example.com", "Ada")).toBe(false);
    expect(isSystemSender("", "")).toBe(false);
    expect(isSystemSender(undefined, undefined)).toBe(false);
  });
});
