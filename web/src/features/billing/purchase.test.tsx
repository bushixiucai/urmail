import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BillingPage } from "./billing-page";
import type { Quote } from "./types";

const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string) => key,
  i18n: { exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/app/providers", () => ({ useI18nLocale: () => ({ locale: "en" }) }));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
afterEach(() => {
  cleanup();
  state.request.mockReset();
  sessionStorage.clear();
});
const flush = async () => {
  await act(async () => {
    for (let i = 0; i < 12; i++) await Promise.resolve();
  });
};
const quote: Quote = {
  tenant_id: 9,
  plan_code: "pro",
  billing_cycle: "quarterly",
  currency: "CNY",
  base_amount_cents: 27000,
  discount_cents: 0,
  prorated_credit_cents: 0,
  final_amount_cents: 27000,
  period_start: "2026-10-24T00:00:00Z",
  period_end: "2027-01-24T00:00:00Z",
  generated_at: "2026-10-24T00:00:00Z",
  expires_at: "2026-10-24T00:30:00Z",
  digest: "a".repeat(64),
};
function setup() {
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("/subscription")
        ? { tenant_id: 9, subscription: null, plan: null }
        : path.endsWith("/plans")
          ? {
              plans: [
                {
                  code: "pro",
                  name: { en: "Pro" },
                  price_cents: 10000,
                  currency: "CNY",
                  mailbox_limit: 5,
                  domain_limit: 2,
                  daily_send_limit: 100,
                  prices: [
                    {
                      currency: "CNY",
                      billing_cycle: "quarterly",
                      amount_cents: 27000,
                      version: 1,
                    },
                  ],
                },
              ],
            }
          : path.endsWith("/quote")
            ? quote
            : path.endsWith("/checkout")
              ? { order: { order_no: "ORD-9", status: "pending" } }
              : {
                  invoices: [],
                  profile: {
                    party: {},
                    tax: {
                      configured: false,
                      rate_basis_points: 0,
                      included_in_total: true,
                    },
                    locale: "en",
                  },
                },
    ),
  );
}
it("shows the configured cycle total and requires confirmation before checkout", async () => {
  setup();
  const view = render(<BillingPage />);
  await flush();
  fireEvent.click(
    view.getByRole("button", { name: "page.billing.cycle_quarterly" }),
  );
  await flush();
  expect(view.getByText("270.00 CNY")).toBeTruthy();
  fireEvent.click(view.getByRole("button", { name: "page.billing.select" }));
  await flush();
  expect(
    state.request.mock.calls.filter(([p]) => p.endsWith("/checkout")),
  ).toHaveLength(0);
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/billing/quote",
    expect.objectContaining({
      body: { plan_code: "pro", billing_cycle: "quarterly", currency: "CNY" },
    }),
  );
  fireEvent.click(
    view.getByRole("button", { name: "page.billing.confirm_payment" }),
  );
  await flush();
  const calls = state.request.mock.calls.filter(([p]) =>
    p.endsWith("/checkout"),
  );
  expect(calls).toHaveLength(1);
  expect(calls[0][1].idempotencyKey).toMatch(/^[a-f0-9-]{36}$/);
  expect(calls[0][1].body).toEqual({ quote });
});
it("restores the original intent on reload and does not reprice its retry", async () => {
  setup();
  sessionStorage.setItem(
    "urmail.purchase.9",
    JSON.stringify({ key: "stable-existing-intent", quote }),
  );
  const view = render(<BillingPage />);
  await flush();
  fireEvent.click(
    view.getByRole("button", { name: "page.billing.confirm_payment" }),
  );
  await flush();
  const calls = state.request.mock.calls.filter(([p]) =>
    p.endsWith("/checkout"),
  );
  expect(calls).toHaveLength(1);
  expect(calls[0][1]).toMatchObject({
    idempotencyKey: "stable-existing-intent",
    body: { quote },
  });
  expect(
    state.request.mock.calls.filter(([p]) => p.endsWith("/quote")),
  ).toHaveLength(0);
});
it("does not infer an unconfigured price for another currency", async () => {
  setup();
  const view = render(<BillingPage />);
  await flush();
  fireEvent.click(view.getByRole("button", { name: "USD" }));
  await flush();
  expect(view.getByText("page.billing.price_unconfigured")).toBeTruthy();
  expect(
    view
      .getByRole("button", { name: "page.billing.select" })
      .hasAttribute("disabled"),
  ).toBe(true);
});
