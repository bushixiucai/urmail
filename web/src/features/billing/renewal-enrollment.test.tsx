import {
  act,
  cleanup,
  fireEvent,
  render,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { BillingLifecycle } from "./lifecycle";
import { ApiRequestError } from "@/lib/api/client";
import { RenewalEnrollment, type RenewalView } from "./renewal-enrollment";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string) => key,
  i18n: { language: "en", exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
let data: RenewalView;
const settle = async () =>
  act(async () => {
    for (let i = 0; i < 15; i++) await Promise.resolve();
  });
beforeEach(() => {
  data = {
    can_manage: true,
    consent_version: "urmail-auto-renew-v1",
    exempt: false,
    exempt_reason: "",
    capabilities: [
      {
        merchant_id: 10,
        provider: "isolated-fixture",
        name: "Approved fixture",
        environment: "sandbox",
        status: "supported",
        reason: "",
        evidence_at: "2026-01-01T00:00:00Z",
        adapter_registered: true,
        merchant_approved: true,
      },
    ],
    bindings: [
      {
        id: 21,
        merchant_id: 10,
        provider: "isolated-fixture",
        label: "Verified authorization ending 42",
        currency: "USD",
        verified_at: "2026-01-01T00:00:00Z",
        expires_at: null,
      },
    ],
    preview: {
      eligible: true,
      reason: "",
      amount_cents: 12345,
      currency: "USD",
      billing_cycle: "monthly",
      period_start: "2030-01-01T00:00:00Z",
      period_end: "2030-02-01T00:00:00Z",
      quote_digest: "a".repeat(64),
    },
    enrollment: null,
    attempts: [],
  };
  state.request.mockImplementation(() =>
    Promise.resolve(structuredClone(data)),
  );
});
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
async function open() {
  const view = render(<RenewalEnrollment />);
  await settle();
  return view;
}
it("enrolls only a server-verified binding after explicit consent to the exact quote", async () => {
  state.request.mockImplementation(
    (_path: string, options?: { method: string }) => {
      if (options?.method === "POST")
        data.enrollment = {
          id: 8,
          state: "active",
          version: 1,
          binding_id: 21,
          consent_version: data.consent_version,
          consented_at: "2026-01-01T00:00:00Z",
          revoked_at: null,
        };
      return Promise.resolve(structuredClone(data));
    },
  );
  const view = await open();
  expect(view.getByText("123.45 USD")).toBeTruthy();
  expect(view.getByText("billing_renewal.channel_ready")).toBeTruthy();
  expect(view.queryByRole("textbox")).toBeNull();
  fireEvent.change(view.getByLabelText("billing_renewal.binding"), {
    target: { value: "21" },
  });
  expect(
    view
      .getByRole("button", { name: "billing_renewal.enable" })
      .matches(":disabled"),
  ).toBe(true);
  fireEvent.click(view.getByLabelText("billing_renewal.enable_consent"));
  fireEvent.click(view.getByRole("button", { name: "billing_renewal.enable" }));
  await waitFor(() =>
    expect(state.request).toHaveBeenCalledWith(
      "/api/v1/billing/auto-renewal/enroll",
      {
        method: "POST",
        body: {
          binding_id: 21,
          consent_version: "urmail-auto-renew-v1",
          quote_digest: "a".repeat(64),
          confirmed: true,
        },
      },
    ),
  );
  await settle();
  expect(view.getByText("billing_renewal.enabled_notice")).toBeTruthy();
});
for (const status of ["unsupported", "unverified"] as const) {
  it(`never permits enrollment for ${status} capability even if a binding is returned`, async () => {
    data.capabilities[0].status = status;
    const view = await open();
    expect(view.getByText(`billing_renewal.support.${status}`)).toBeTruthy();
    expect(
      view.queryByRole("option", { name: /Verified authorization/ }),
    ).toBeNull();
    expect(
      view
        .getByRole("button", { name: "billing_renewal.enable" })
        .matches(":disabled"),
    ).toBe(true);
  });
}
it("rejects expired, foreign or unapproved bindings without exposing an opaque reference input", async () => {
  data.bindings.push({ ...data.bindings[0], id: 22, merchant_id: 99 });
  data.bindings[0].expires_at = "2000-01-01T00:00:00Z";
  const view = await open();
  expect(
    view.getByLabelText("billing_renewal.binding").querySelectorAll("option"),
  ).toHaveLength(1);
  expect(view.getByText("billing_renewal.no_binding")).toBeTruthy();
  expect(view.queryByRole("textbox")).toBeNull();
});
it("renders free and service exemption without creating payment intent", async () => {
  data.exempt = true;
  data.exempt_reason = "service_entitlement";
  data.preview!.eligible = false;
  const view = await open();
  expect(view.getByText("billing_renewal.exempt")).toBeTruthy();
  expect(
    view
      .getByRole("button", { name: "billing_renewal.enable" })
      .matches(":disabled"),
  ).toBe(true);
  expect(
    state.request.mock.calls.every(
      ([, options]) => !options?.method || options.method === "GET",
    ),
  ).toBe(true);
});
it("requires cancellation consent and preserves real in-flight payment state", async () => {
  data.enrollment = {
    id: 8,
    state: "active",
    version: 1,
    binding_id: 21,
    consent_version: data.consent_version,
    consented_at: "2026-01-01T00:00:00Z",
    revoked_at: null,
  };
  data.attempts = [
    {
      id: 99,
      state: "uncertain",
      period_end: "2030-01-01T00:00:00Z",
      last_error: "",
      in_flight: true,
      amount_cents_exact: "23456",
      currency: "CNY",
    },
  ];
  state.request.mockImplementation((path: string) => {
    if (path.endsWith("/cancel")) {
      data.enrollment!.state = "revoked";
      return Promise.resolve({ enrollment: data.enrollment, in_flight: true });
    }
    return Promise.resolve(structuredClone(data));
  });
  const view = await open();
  expect(
    view
      .getByRole("button", { name: "billing_renewal.disable" })
      .matches(":disabled"),
  ).toBe(true);
  fireEvent.click(view.getByLabelText("billing_renewal.disable_consent"));
  fireEvent.click(
    view.getByRole("button", { name: "billing_renewal.disable" }),
  );
  await settle();
  expect(view.getByText("billing_renewal.disabled_in_flight")).toBeTruthy();
  expect(view.getByText("234.56 CNY")).toBeTruthy();
  expect(view.getByText("123.45 USD")).toBeTruthy();
  expect(view.getByText(/billing_renewal\.attempt\.uncertain/)).toBeTruthy();
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/billing/auto-renewal/cancel",
    { method: "POST", body: {} },
  );
  state.request.mockRejectedValue(
    new ApiRequestError(403, { error: { code: "FORBIDDEN_ROLE" } }),
  );
  fireEvent.click(
    view.getByRole("button", { name: "billing_renewal.refresh" }),
  );
  await settle();
  expect(view.queryByText("billing_renewal.disabled_in_flight")).toBeNull();
  expect(view.queryByText("234.56 CNY")).toBeNull();
  expect(view.getByText("billing_renewal.forbidden")).toBeTruthy();
});
it("does not round large exact amounts or enable unknown amounts", async () => {
  data.preview!.amount_cents_exact = "900719925474099123";
  const view = await open();
  expect(view.getByText("9007199254740991.23 USD")).toBeTruthy();
});
it("shows permission denial and a manual renewal path", async () => {
  state.request.mockRejectedValue(
    new ApiRequestError(403, { error: { code: "FORBIDDEN" } }),
  );
  const view = await open();
  expect(view.getByText("billing_renewal.forbidden")).toBeTruthy();
  expect(
    view
      .getByRole("link", { name: "billing_renewal.manual_renewal" })
      .getAttribute("href"),
  ).toBe("#billing-purchase");
  expect(
    view.queryByRole("button", { name: "billing_renewal.enable" }),
  ).toBeNull();
});

it("disables stale authorization controls after a failed refresh", async () => {
  const view = await open();
  fireEvent.change(view.getByLabelText("billing_renewal.binding"), {
    target: { value: "21" },
  });
  fireEvent.click(view.getByLabelText("billing_renewal.enable_consent"));
  state.request.mockRejectedValue(new Error("network unavailable"));
  fireEvent.click(
    view.getByRole("button", { name: "billing_renewal.refresh" }),
  );
  await settle();
  expect(
    view
      .getByRole("button", { name: "billing_renewal.enable" })
      .matches(":disabled"),
  ).toBe(true);
  expect(view.getByText("billing_renewal.unavailable")).toBeTruthy();
});

it("refuses an unsafe rounded numeric amount without an exact server value", async () => {
  data.preview!.amount_cents = Number.MAX_SAFE_INTEGER + 1;
  const view = await open();
  expect(view.getByText("billing_renewal.amount_unavailable")).toBeTruthy();
  fireEvent.change(view.getByLabelText("billing_renewal.binding"), {
    target: { value: "21" },
  });
  expect(
    view
      .getByRole("button", { name: "billing_renewal.enable" })
      .matches(":disabled"),
  ).toBe(true);
});

it("refreshes actual enrollment after subscription cancellation instead of retaining stale active consent", async () => {
  data.enrollment = {
    id: 8,
    state: "active",
    version: 1,
    binding_id: 21,
    consent_version: data.consent_version,
    consented_at: "2026-01-01T00:00:00Z",
    revoked_at: null,
  };
  const subscription = {
    plan_id: "paid",
    status: "active",
    period_start: "2026-01-01T00:00:00Z",
    period_end: "2030-01-01T00:00:00Z",
    entitlement_source: "standard",
    cancel_at_period_end: false,
  };
  state.request.mockImplementation((path: string) => {
    if (path.endsWith("/auto-renewal"))
      return Promise.resolve(structuredClone(data));
    if (path.endsWith("/subscription/cancel")) {
      subscription.cancel_at_period_end = true;
      data.enrollment!.state = "revoked";
      return Promise.resolve({ subscription });
    }
    if (path.endsWith("/subscription"))
      return Promise.resolve({
        subscription: structuredClone(subscription),
        scheduled_change: null,
      });
    return Promise.resolve({
      orders: [],
      payments: [],
      refunds: [],
      plans: [],
      domains: [],
    });
  });
  const view = render(<BillingLifecycle />);
  await view.findByRole("button", { name: "billing_renewal.disable" });
  fireEvent.click(
    view.getByRole("button", { name: "billing_lifecycle.cancel_subscription" }),
  );
  await view.findByRole("button", { name: "billing_renewal.enable" });
  expect(
    view.queryByRole("button", { name: "billing_renewal.disable" }),
  ).toBeNull();
  expect(
    state.request.mock.calls.filter(([path]) => path.endsWith("/auto-renewal")),
  ).toHaveLength(2);
});

it("holds authorization selection until refresh completes so a late response cannot erase a new consent choice", async () => {
  const view = await open();
  let complete!: (value: RenewalView) => void;
  state.request.mockReturnValue(
    new Promise<RenewalView>((resolve) => {
      complete = resolve;
    }),
  );
  fireEvent.click(
    view.getByRole("button", { name: "billing_renewal.refresh" }),
  );
  expect(
    view.getByLabelText("billing_renewal.binding").matches(":disabled"),
  ).toBe(true);
  complete(structuredClone(data));
  await settle();
  fireEvent.change(view.getByLabelText("billing_renewal.binding"), {
    target: { value: "21" },
  });
  expect(view.getByLabelText("billing_renewal.enable_consent")).toBeTruthy();
});
