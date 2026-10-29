import { afterEach, describe, expect, it, vi } from "vitest";
import {
  apiErrorSchema,
  ApiRequestError,
  checkoutDestination,
  clearAuth,
  getCSRFToken,
  getSessionToken,
  request,
  setCSRFToken,
  setSessionToken,
} from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});
describe("API error contracts", () => {
  it("preserves code-only business errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: { code: "INVALID_PARAMS" } }), {
          status: 400,
        }),
      ),
    );
    await expect(request("/api/v1/test")).rejects.toMatchObject({
      status: 400,
      code: "INVALID_PARAMS",
    });
  });
  it("retains key and interpolation parameters", () => {
    const body = apiErrorSchema.parse({
      error: { code: "QUOTA", message: "quota.exceeded", params: { limit: 3 } },
    });
    expect(new ApiRequestError(403, body)).toMatchObject({
      messageKey: "quota.exceeded",
      params: { limit: 3 },
    });
  });
  it("normalizes malformed upstream errors without exposing HTML", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("<secret>", { status: 502 })),
    );
    await expect(request("/api/v1/test")).rejects.toMatchObject({
      code: "INTERNAL_ERROR",
      status: 502,
    });
  });
  it("rejects injected or unrelated checkout destinations", () => {
    for (const value of [
      "javascript:alert(1)",
      "https://checkout.stripe.com.evil.test/x",
      "https://user@checkout.stripe.com/x",
      "http://checkout.stripe.com/x",
    ]) {
      expect(() => checkoutDestination(value)).toThrow();
    }
    expect(checkoutDestination("https://checkout.stripe.com/c/pay/test")).toBe(
      "https://checkout.stripe.com/c/pay/test",
    );
  });

  describe("credential storage and unified logout", () => {
    it("stores tokens in localStorage by default and prefers localStorage over sessionStorage", () => {
      setSessionToken("tok-local");
      setCSRFToken("csrf-local");
      expect(window.localStorage.getItem("urmail.session-token")).toBe("tok-local");
      expect(window.localStorage.getItem("urmail.csrf-token")).toBe("csrf-local");
      expect(getSessionToken()).toBe("tok-local");
      expect(getCSRFToken()).toBe("csrf-local");

      // fallback to sessionStorage when not in localStorage
      window.localStorage.clear();
      window.sessionStorage.setItem("urmail.session-token", "tok-session");
      window.sessionStorage.setItem("urmail.csrf-token", "csrf-session");
      expect(getSessionToken()).toBe("tok-session");
      expect(getCSRFToken()).toBe("csrf-session");

      // localStorage takes priority
      window.localStorage.setItem("urmail.session-token", "tok-local-priority");
      expect(getSessionToken()).toBe("tok-local-priority");
    });

    it("supports remember=false to store exclusively in sessionStorage", () => {
      setSessionToken("tok-temp", false);
      setCSRFToken("csrf-temp", false);
      expect(window.sessionStorage.getItem("urmail.session-token")).toBe("tok-temp");
      expect(window.sessionStorage.getItem("urmail.csrf-token")).toBe("csrf-temp");
      expect(window.localStorage.getItem("urmail.session-token")).toBeNull();
      expect(window.localStorage.getItem("urmail.csrf-token")).toBeNull();
    });

    it("clearAuth cleans both localStorage, sessionStorage and mailbox token", () => {
      window.localStorage.setItem("urmail.session-token", "tok1");
      window.localStorage.setItem("urmail.csrf-token", "csrf1");
      window.localStorage.setItem("urmail.mailbox-token", "mbox1");
      window.sessionStorage.setItem("urmail.session-token", "tok2");
      window.sessionStorage.setItem("urmail.csrf-token", "csrf2");
      window.sessionStorage.setItem("urmail.mailbox-token", "mbox2");

      clearAuth();

      expect(window.localStorage.getItem("urmail.session-token")).toBeNull();
      expect(window.localStorage.getItem("urmail.csrf-token")).toBeNull();
      expect(window.localStorage.getItem("urmail.mailbox-token")).toBeNull();
      expect(window.sessionStorage.getItem("urmail.session-token")).toBeNull();
      expect(window.sessionStorage.getItem("urmail.csrf-token")).toBeNull();
      expect(window.sessionStorage.getItem("urmail.mailbox-token")).toBeNull();
    });
  });
});
