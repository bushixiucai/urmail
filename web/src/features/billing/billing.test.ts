import { describe, expect, it } from "vitest";
import {
  BILLING_CYCLES,
  buildCheckoutBody,
  buildQuoteRequest,
  type Quote,
} from "./types";

const signedQuote: Quote = {
  tenant_id: 1,
  plan_code: "pro",
  billing_cycle: "annual",
  currency: "CNY",
  base_amount_cents: 120000,
  discount_cents: 12000,
  prorated_credit_cents: 0,
  final_amount_cents: 108000,
  period_start: "2026-01-01T00:00:00Z",
  period_end: "2026-12-31T23:59:59Z",
  generated_at: "2025-12-31T00:00:00Z",
  expires_at: "2026-01-01T01:00:00Z",
  digest: "abc",
};

describe("buildQuoteRequest", () => {
  it("sends every supported billing cycle", () => {
    expect(BILLING_CYCLES).toEqual(["monthly", "quarterly", "annual"]);
    for (const cycle of BILLING_CYCLES) {
      expect(buildQuoteRequest("pro", cycle)).toEqual({
        plan_code: "pro",
        billing_cycle: cycle,
      });
    }
  });

  it("includes the coupon when provided", () => {
    expect(buildQuoteRequest("pro", "quarterly", "SAVE10")).toEqual({
      plan_code: "pro",
      billing_cycle: "quarterly",
      coupon_code: "SAVE10",
    });
  });
});

describe("buildCheckoutBody", () => {
  it("prefers the signed quote when one is loaded", () => {
    expect(buildCheckoutBody(signedQuote, "pro", "monthly")).toEqual({
      quote: signedQuote,
    });
  });

  it("falls back to the legacy inline body without a quote", () => {
    expect(buildCheckoutBody(null, "pro", "quarterly")).toEqual({
      plan_code: "pro",
      billing_cycle: "quarterly",
    });
    expect(buildCheckoutBody(null, "pro", "annual", "SAVE10")).toEqual({
      plan_code: "pro",
      billing_cycle: "annual",
      coupon_code: "SAVE10",
    });
  });
});
