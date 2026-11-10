import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiRequestError,
  clearAuth,
  clearEtagCacheForTests,
  getCSRFToken,
  getSessionToken,
  parseApiErrorPayload,
  request,
  setCSRFToken,
  setSessionToken,
} from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
  clearEtagCacheForTests();
  window.sessionStorage.clear();
});

describe("ETag revalidation (M4①)", () => {
  it("stores a 200 ETag payload and reuses it on 304 without re-reading the body", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ hello: "world" }), {
          status: 200,
          headers: { ETag: '"v1"' },
        }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal("fetch", fetchMock);

    const first = await request<{ hello: string }>("/api/v1/thing");
    expect(first).toEqual({ hello: "world" });

    const second = await request<{ hello: string }>("/api/v1/thing");
    expect(second).toEqual({ hello: "world" });

    const revalidateInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(
      (revalidateInit.headers as Record<string, string>)["If-None-Match"],
    ).toBe('"v1"');
  });

  it("replaces the cached payload when the ETag changes", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ v: 1 }), {
          status: 200,
          headers: { ETag: '"v1"' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ v: 2 }), {
          status: 200,
          headers: { ETag: '"v2"' },
        }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 304 }));
    vi.stubGlobal("fetch", fetchMock);

    await request<{ v: number }>("/api/v1/thing");
    const fresh = await request<{ v: number }>("/api/v1/thing");
    expect(fresh).toEqual({ v: 2 });

    const third = await request<{ v: number }>("/api/v1/thing");
    expect(third).toEqual({ v: 2 });
    const thirdInit = fetchMock.mock.calls[2][1] as RequestInit;
    expect((thirdInit.headers as Record<string, string>)["If-None-Match"]).toBe(
      '"v2"',
    );
  });

  it("does not attach If-None-Match to non-GET requests", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { ETag: '"v1"' },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await request("/api/v1/thing", { method: "POST", body: { x: 1 } });
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(
      (init.headers as Record<string, string>)["If-None-Match"],
    ).toBeUndefined();
  });
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
    const body = parseApiErrorPayload({
      error: { code: "QUOTA", message: "quota.exceeded", params: { limit: 3 } },
    });
    expect(body).not.toBeNull();
    expect(new ApiRequestError(403, body!)).toMatchObject({
      messageKey: "quota.exceeded",
      params: { limit: 3 },
    });
  });
  it("parseApiErrorPayload accepts the documented envelope shapes", () => {
    expect(parseApiErrorPayload({ error: { code: "" } })).toMatchObject({
      error: { code: "" },
    });
    expect(
      parseApiErrorPayload({
        error: { code: "X", details: { any: "thing" }, extra: "dropped" },
      }),
    ).toMatchObject({ error: { code: "X", details: { any: "thing" } } });
    expect(
      parseApiErrorPayload({
        error: { code: "X", params: { a: 1 } },
      })?.error.params,
    ).toEqual({ a: 1 });
  });
  it("parseApiErrorPayload rejects malformed envelopes", () => {
    expect(parseApiErrorPayload(null)).toBeNull();
    expect(parseApiErrorPayload("<html>")).toBeNull();
    expect(parseApiErrorPayload({})).toBeNull();
    expect(parseApiErrorPayload({ error: {} })).toBeNull();
    expect(parseApiErrorPayload({ error: { code: 5 } })).toBeNull();
    expect(
      parseApiErrorPayload({ error: { code: "X", message: 9 } }),
    ).toBeNull();
    expect(
      parseApiErrorPayload({ error: { code: "X", params: "nope" } }),
    ).toBeNull();
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
  describe("credential storage and unified logout", () => {
    it("stores tokens in localStorage by default and prefers localStorage over sessionStorage", () => {
      setSessionToken("tok-local");
      setCSRFToken("csrf-local");
      expect(window.localStorage.getItem("urmail.session-token")).toBe(
        "tok-local",
      );
      expect(window.localStorage.getItem("urmail.csrf-token")).toBe(
        "csrf-local",
      );
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
      expect(window.sessionStorage.getItem("urmail.session-token")).toBe(
        "tok-temp",
      );
      expect(window.sessionStorage.getItem("urmail.csrf-token")).toBe(
        "csrf-temp",
      );
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
