import { afterEach, describe, expect, it, vi } from "vitest";
import {
  apiErrorSchema,
  ApiRequestError,
  checkoutDestination,
  request,
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
});
