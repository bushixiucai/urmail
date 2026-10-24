import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { FinancialReviewAdmin } from "./financial-review-admin";
import { NotificationAdmin } from "./notification-admin";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string) => key,
  i18n: { exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
async function settle() {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve();
  });
}
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
function fixture() {
  state.request.mockImplementation(async (path: string) =>
    path.endsWith("payment-reviews")
      ? {
          reviews: [
            {
              id: 3,
              tenant_id: 1,
              order_id: 2,
              provider: "stripe",
              received_amount_cents: 100,
              currency: "USD",
              received_currency: "USD",
              expected_amount_cents: 100,
              expected_currency: "USD",
              reason: "late receipt",
            },
          ],
        }
      : path.endsWith("/refunds")
        ? {
            refunds: [
              {
                id: 4,
                tenant_id: 1,
                order_id: 2,
                refund_no: "REF1",
                requested_amount_cents: 50,
                currency: "USD",
                status: "pending",
                reason: "partial",
                entitlement_action: "none",
              },
            ],
          }
        : {},
  );
}
it("loads read-only and submits only an explicit receipt decision with a reason", async () => {
  fixture();
  const view = render(<FinancialReviewAdmin />);
  await settle();
  expect(state.request.mock.calls.every(([, o]) => !o?.method)).toBe(true);
  fireEvent.click(view.getByRole("button", { name: "finance.review.handle" }));
  const reason = view.getByLabelText("finance.reason");
  fireEvent.change(reason, {
    target: { value: "Confirmed receipt and customer request" },
  });
  fireEvent.submit(reason.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(([, o]) => o?.method === "POST");
  expect(call?.[0]).toBe("/api/v1/platform/payment-reviews/3/resolve");
  expect(call?.[1].body).toMatchObject({ action: "request_refund" });
  expect(call?.[0]).not.toContain("approve");
});
it("keeps entitlement changes off for an explicitly approved partial refund", async () => {
  fixture();
  const view = render(<FinancialReviewAdmin />);
  await settle();
  fireEvent.click(view.getByRole("button", { name: "finance.approval" }));
  fireEvent.change(view.getByLabelText("finance.decision"), {
    target: { value: "approve" },
  });
  const reason = view.getByLabelText("finance.reason");
  fireEvent.change(reason, {
    target: { value: "Approved partial original-route refund" },
  });
  fireEvent.submit(reason.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(([, o]) => o?.method === "POST");
  expect(call?.[0]).toBe("/api/v1/platform/refunds/4/approve");
  expect(call?.[1].body).toMatchObject({
    decision: "approve",
    amount_cents: 50,
    entitlement_action: "none",
  });
  expect(call?.[1].body).not.toHaveProperty("effective_at");
});
it("queries existing refunds without approving or requesting funds", async () => {
  fixture();
  const view = render(<FinancialReviewAdmin />);
  await settle();
  fireEvent.click(view.getByRole("button", { name: "finance.query" }));
  const reason = view.getByLabelText("finance.reason");
  fireEvent.change(reason, {
    target: { value: "Reconcile existing provider refund only" },
  });
  fireEvent.submit(reason.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(([, o]) => o?.method === "POST");
  expect(call?.[0]).toBe("/api/v1/platform/refunds/4/query");
  expect(call?.[1].body).toEqual({
    reason: "Reconcile existing provider refund only",
    confirm_observed: false,
  });
});
it("never prefills or sends relay credentials on page load", async () => {
  state.request.mockImplementation(async (path: string) =>
    path.endsWith("reminder-policy") ? { days: [7, 3, 1, 0] } : { alerts: [] },
  );
  const view = render(<NotificationAdmin />);
  await settle();
  expect(
    (view.getByLabelText("notify.admin.password") as HTMLInputElement).value,
  ).toBe("");
  expect(state.request.mock.calls).toHaveLength(2);
  expect(
    state.request.mock.calls.some(
      ([path]) => path === "/api/v1/platform/alerts",
    ),
  ).toBe(true);
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
});

it("renders exact provider cash strings without rounding through JavaScript numbers", async () => {
  fixture();
  state.request.mockImplementation(async (path: string) =>
    path.endsWith("payment-reviews")
      ? {
          reviews: [
            {
              id: 3,
              tenant_id: 1,
              order_id: 2,
              provider: "stripe",
              received_amount_cents: 9223372036854776000,
              received_amount_cents_exact: "9223372036854775807",
              received_currency: "USD",
              expected_amount_cents: 100,
              expected_currency: "USD",
              reason: "large provider cash",
            },
          ],
        }
      : { refunds: [] },
  );
  const view = render(<FinancialReviewAdmin />);
  await settle();
  expect(view.getByText(/92233720368547758\.07/)).toBeTruthy();
});
