import {
  act,
  cleanup,
  fireEvent,
  render,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BillingLifecycle } from "./lifecycle";
const state = vi.hoisted(() => ({ request: vi.fn(), t: (key: string) => key }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("preserves non-expiring mail rights without offering cancellation, scheduling or trial replacement", async () => {
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("/subscription")
        ? {
            subscription: {
              plan_id: "migration_legacy",
              status: "active",
              period_start: "2026-01-01T00:00:00Z",
              period_end: null,
              entitlement_source: "migration_legacy",
              cancel_at_period_end: false,
            },
          }
        : path.endsWith("/plans")
          ? { plans: [{ code: "commercial", name: { en: "Commercial" } }] }
          : path.endsWith("/domains")
            ? { domains: [] }
            : { orders: [], payments: [], refunds: [] },
    ),
  );
  const view = render(<BillingLifecycle />);
  await act(async () => {
    for (let n = 0; n < 12; n++) await Promise.resolve();
  });
  expect(view.getByText(/billing_lifecycle.migration_preserved/)).toBeTruthy();
  expect(view.getByText("billing_lifecycle.migration_purchase")).toBeTruthy();
  expect(
    view.queryByRole("button", {
      name: "billing_lifecycle.cancel_subscription",
    }),
  ).toBeNull();
  expect(
    view.getByRole("button", { name: "billing_lifecycle.schedule" }),
  ).toHaveProperty("disabled", true);
  expect(
    view.getByRole("button", { name: "billing_lifecycle.redeem" }),
  ).toHaveProperty("disabled", true);
  expect(
    state.request.mock.calls.every(
      ([, options]) => !options?.method || options.method === "GET",
    ),
  ).toBe(true);
});

it("re-enables automatic renewal explicitly without changing paid dates", async () => {
  let canceled = true;
  state.request.mockImplementation(
    (
      path: string,
      options?: { method?: string; body?: { cancel_at_period_end?: boolean } },
    ) => {
      if (path.endsWith("/subscription/cancel")) {
        canceled = options?.body?.cancel_at_period_end ?? true;
        return Promise.resolve({});
      }
      return Promise.resolve(
        path.endsWith("/subscription")
          ? {
              subscription: {
                plan_id: "paid",
                status: "active",
                period_end: "2027-01-01T00:00:00Z",
                entitlement_source: "paid",
                cancel_at_period_end: canceled,
              },
            }
          : { orders: [], payments: [], refunds: [], plans: [], domains: [] },
      );
    },
  );
  const view = render(<BillingLifecycle />);
  const resume = await view.findByRole("button", {
    name: "billing_lifecycle.resume_subscription",
  });
  fireEvent.click(resume);
  await waitFor(() =>
    expect(state.request).toHaveBeenCalledWith(
      "/api/v1/billing/subscription/cancel",
      { method: "POST", body: { cancel_at_period_end: false } },
    ),
  );
  await view.findByRole("button", {
    name: "billing_lifecycle.cancel_subscription",
  });
  expect(view.getByText(/2027-01-01T00:00:00Z/)).toBeTruthy();
});
