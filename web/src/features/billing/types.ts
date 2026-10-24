/**
 * Shared billing request/response types. Mirrors the backend quote payload
 * (integer cents) returned by POST /api/v1/billing/quote and accepted by
 * POST /api/v1/billing/checkout.
 */

export type BillingCycle = "monthly" | "quarterly" | "annual";

export const BILLING_CYCLES: readonly BillingCycle[] = [
  "monthly",
  "quarterly",
  "annual",
];

export interface PlanPrice {
  currency: string;
  billing_cycle: BillingCycle;
  amount_cents: number;
  version: number;
}

export interface Plan {
  prices?: PlanPrice[];
  code: string;
  name: Record<string, string>;
  description?: Record<string, string>;
  price_cents: number;
  currency: string;
  mailbox_limit: number;
  domain_limit: number;
  daily_send_limit: number;
}

export interface Subscription {
  plan_id: string;
  status: string;
  period_start: string;
  period_end: string | null;
  entitlement_source?: "standard" | "migration_legacy";
  cancel_at_period_end: boolean;
}

export interface Invoice {
  id: number;
  number: string;
  amount_cents: number;
  currency: string;
  status: string;
  period_start: string;
  period_end: string;
}

/** Signed quote returned by the backend; sent back verbatim at checkout. */
export interface Quote {
  current_entitlement_source?: "standard" | "migration_legacy";
  calendar?: {
    timezone: string;
    anchor_at: string;
    version: number;
    reanchor: boolean;
    provisional: boolean;
  };
  tenant_id: number;
  coupon_code?: string;
  plan_code: string;
  billing_cycle: BillingCycle;
  currency: string;
  base_amount_cents: number;
  discount_cents: number;
  prorated_credit_cents: number;
  final_amount_cents: number;
  period_start: string;
  period_end: string;
  generated_at: string;
  expires_at: string;
  digest: string;
}

export interface QuoteRequest {
  plan_code: string;
  billing_cycle: BillingCycle;
  coupon_code?: string;
  currency?: string;
}

export type CheckoutRequest =
  | { quote: Quote }
  | {
      plan_code: string;
      billing_cycle?: BillingCycle;
      coupon_code?: string;
    };

export function buildQuoteRequest(
  planCode: string,
  billingCycle: BillingCycle,
  couponCode?: string,
  currency?: string,
): QuoteRequest {
  const body: QuoteRequest = {
    plan_code: planCode,
    billing_cycle: billingCycle,
  };
  if (couponCode) body.coupon_code = couponCode;
  if (currency) body.currency = currency;
  return body;
}

/**
 * Checkout prefers a signed quote when one is already loaded; otherwise it
 * falls back to the legacy inline body.
 */
export function buildCheckoutBody(
  quote: Quote | null,
  planCode: string,
  billingCycle: BillingCycle,
  couponCode?: string,
): CheckoutRequest {
  if (quote) return { quote };
  const body: CheckoutRequest = {
    plan_code: planCode,
    billing_cycle: billingCycle,
  };
  if (couponCode) (body as { coupon_code?: string }).coupon_code = couponCode;
  return body;
}

/** Prices are independent configured totals, never an inferred monthly multiplier. */
export function configuredPrice(
  plan: Plan,
  cycle: BillingCycle,
  currency: string,
): number | null {
  const price = plan.prices?.find(
    (p) => p.currency === currency && p.billing_cycle === cycle,
  );
  if (price) return price.amount_cents;
  if (cycle === "monthly" && currency === plan.currency)
    return plan.price_cents;
  return null;
}
