import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BillingPage } from "./billing-page";

const state = vi.hoisted(() => ({
  request: vi.fn(),
  lifecycleLoads: 0,
  t: (key: string, params?: { timezone?: string }) =>
    params?.timezone ? `${key}:${params.timezone}` : key,
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t }),
}));
vi.mock("@/app/providers", () => ({ useI18nLocale: () => ({ locale: "en" }) }));
vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({ currentTenant: { id: 1, role: "owner" } }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
vi.mock("./document-profile", () => ({ DocumentProfile: () => null }));
vi.mock("./lifecycle", () => ({
  BillingLifecycle: () => {
    useEffect(() => {
      state.lifecycleLoads++;
    }, []);
    return <div>lifecycle</div>;
  },
}));
afterEach(() => {
  cleanup();
  state.request.mockReset();
  state.lifecycleLoads = 0;
  sessionStorage.clear();
});

const plan = {
  code: "paid",
  name: { en: "Paid plan" },
  prices: [
    {
      currency: "CNY",
      billing_cycle: "monthly",
      amount_cents: 100,
      version: 1,
    },
  ],
  currency: "CNY",
  price_cents: 100,
  mailbox_limit: 5,
  domain_limit: 2,
  daily_send_limit: 100,
};
const quote = {
  tenant_id: 1,
  plan_code: "paid",
  billing_cycle: "monthly",
  currency: "CNY",
  base_amount_cents: 100,
  discount_cents: 100,
  prorated_credit_cents: 0,
  final_amount_cents: 0,
  period_start: "2027-01-01T00:00:00Z",
  period_end: "2027-02-01T00:00:00Z",
  generated_at: "2026-10-24T00:00:00Z",
  expires_at: "2026-10-25T00:00:00Z",
  digest: "signed-future-renewal",
};

describe("confirmed checkout uses its real settlement state", () => {
  for (const [status, refreshFailure] of [
    ["paid", false],
    ["pending", false],
    ["paid", true],
  ] as const) {
    it(`${status} refreshFailure=${refreshFailure} preserves the correct notice, recovery key and refresh behavior`, async () => {
      let settled = false;
      state.request.mockImplementation(
        async (path: string, options?: { method?: string }) => {
          if (path.endsWith("/quote")) return quote;
          if (path.endsWith("/checkout") && options?.method === "POST") {
            settled = status === "paid";
            return { order: { order_no: "ZERO-FUTURE-RENEWAL", status } };
          }
          if (path.endsWith("/subscription") && settled && refreshFailure)
            throw new Error("Refresh connection lost");
          if (path.endsWith("/subscription"))
            return {
              tenant_id: 1,
              can_manage_billing: true,
              plan: {
                ...plan,
                name: {
                  en: settled ? "Refreshed current plan" : "Current plan",
                },
              },
              subscription: {
                plan_id: "paid",
                status: "active",
                period_start: "2026-10-01T00:00:00Z",
                period_end: settled
                  ? "2027-02-01T00:00:00Z"
                  : "2027-01-01T00:00:00Z",
              },
            };
          if (path.endsWith("/plans")) return { plans: [plan] };
          if (path.endsWith("/invoices"))
            return {
              invoices: settled
                ? [
                    {
                      id: 1,
                      number: "INV-PAID-REFRESHED",
                      amount_cents: 0,
                      currency: "CNY",
                      status: "paid",
                      period_start: quote.period_start,
                      period_end: quote.period_end,
                    },
                  ]
                : [],
            };
          if (path.endsWith("/payment-methods")) return { methods: [] };
          return { domains: [] };
        },
      );
      let view = render(<BillingPage />);
      await waitFor(() =>
        expect(
          (
            view.getByRole("button", {
              name: "page.billing.select",
            }) as HTMLButtonElement
          ).disabled,
        ).toBe(false),
      );
      fireEvent.click(
        view.getByRole("button", { name: "page.billing.select" }),
      );
      await waitFor(() =>
        expect(
          (
            view.getByRole("button", {
              name: "page.billing.confirm_payment",
            }) as HTMLButtonElement
          ).disabled,
        ).toBe(false),
      );
      const persisted = JSON.parse(
        sessionStorage.getItem("urmail.purchase.1")!,
      );
      expect(persisted.quote).toEqual(quote);
      expect(persisted.key).toMatch(/[a-f0-9-]{36}/);
      // A page refresh restores the server quote before any checkout request exists.
      view.unmount();
      view = render(<BillingPage />);
      await waitFor(() =>
        expect(
          (
            view.getByRole("button", {
              name: "page.billing.confirm_payment",
            }) as HTMLButtonElement
          ).disabled,
        ).toBe(false),
      );
      expect(
        state.request.mock.calls.filter(([path]) => path.endsWith("/quote")),
      ).toHaveLength(1);
      fireEvent.click(
        view.getByRole("button", { name: "page.billing.confirm_payment" }),
      );
      await waitFor(() =>
        expect(
          view.getByText(
            status === "paid"
              ? refreshFailure
                ? "page.billing.completed_refresh_failed"
                : "page.billing.completed_order"
              : "page.billing.manual_order",
          ),
        ).not.toBeNull(),
      );
      const checkout = state.request.mock.calls.find(([path]) =>
        path.endsWith("/checkout"),
      );
      expect(checkout?.[1]).toMatchObject({
        idempotencyKey: persisted.key,
        body: { quote },
      });
      if (status === "paid" && !refreshFailure) {
        await waitFor(() =>
          expect(view.getByText("Refreshed current plan")).not.toBeNull(),
        );
        await waitFor(() =>
          expect(view.getByText("INV-PAID-REFRESHED")).not.toBeNull(),
        );
        expect(sessionStorage.getItem("urmail.purchase.1")).toBeNull();
        expect(view.queryByText("page.billing.manual_order")).toBeNull();
        expect(
          view.queryByRole("button", { name: "page.billing.confirm_payment" }),
        ).toBeNull();
        expect(state.lifecycleLoads).toBe(3);
      } else if (status === "paid") {
        expect(sessionStorage.getItem("urmail.purchase.1")).toBeNull();
        expect(
          view.queryByRole("button", { name: "page.billing.confirm_payment" }),
        ).toBeNull();
        expect(
          state.request.mock.calls.filter(([path]) =>
            path.endsWith("/checkout"),
          ),
        ).toHaveLength(1);
        expect(view.queryByText("page.billing.manual_order")).toBeNull();
      } else {
        expect(
          JSON.parse(sessionStorage.getItem("urmail.purchase.1")!).key,
        ).toBe(persisted.key);
        expect(state.lifecycleLoads).toBe(2);
        expect(view.queryByText("page.billing.completed_order")).toBeNull();
      }
    });
  }
});

for (const provisional of [false, true]) {
  it(`displays the signed billing timezone and provisional=${provisional} notice on restored quotes`, async () => {
    sessionStorage.setItem(
      "urmail.purchase.1",
      JSON.stringify({
        key: "calendar-display",
        quote: {
          ...quote,
          calendar: {
            timezone: "America/New_York",
            anchor_at: quote.period_start,
            version: 1,
            reanchor: provisional,
            provisional,
          },
        },
      }),
    );
    state.request.mockImplementation(async (path: string) =>
      path.endsWith("/subscription")
        ? { tenant_id: 1, can_manage_billing: true, subscription: null, plan }
        : path.endsWith("/plans")
          ? { plans: [plan] }
          : path.endsWith("/payment-methods")
            ? { methods: [] }
            : { domains: [], invoices: [] },
    );
    const view = render(<BillingPage />);
    await waitFor(() =>
      expect(view.getByTestId("quote-calendar")).not.toBeNull(),
    );
    expect(view.getByTestId("quote-calendar").textContent).toContain(
      "page.billing.calendar",
    );
    expect(view.getByText(/Dec 31, 2026/).textContent).toContain("7:00");
    expect(view.queryByText("page.billing.calendar_provisional") !== null).toBe(
      provisional,
    );
    expect(
      state.request.mock.calls.some(
        ([, options]) => options?.method === "POST",
      ),
    ).toBe(false);
  });
}
