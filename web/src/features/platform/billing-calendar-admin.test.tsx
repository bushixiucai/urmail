import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BillingCalendarAdmin } from "./billing-calendar-admin";
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
const settle = async () => {
  await act(async () => {
    for (let i = 0; i < 16; i++) await Promise.resolve();
  });
};
const calendar = {
  timezone: "Asia/Shanghai",
  first_paid_at: "2024-01-31T01:00:00Z",
  anchor_at: "2024-01-31T01:00:00Z",
  version: 1,
  source: "manual_legacy",
};
const tenants = [
  { id: 1, name: "Legacy tenant" },
  { id: 2, name: "New tenant" },
];
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("does not auto-guess a legacy timezone or first payment and saves only explicit facts", async () => {
  state.request.mockImplementation(
    (_path: string, options?: { method?: string }) =>
      Promise.resolve({
        tenant_id: 1,
        calendar: options?.method === "PUT" ? calendar : null,
        review_required: options?.method !== "PUT",
        current_business_timezone: "America/New_York",
      }),
  );
  const view = render(<BillingCalendarAdmin tenants={tenants} />);
  expect(state.request).not.toHaveBeenCalled();
  fireEvent.change(view.getByRole("combobox"), { target: { value: "1" } });
  await settle();
  const zone = view.container.querySelector<HTMLInputElement>(
      'input[name="timezone"]',
    )!,
    first = view.container.querySelector<HTMLInputElement>(
      'input[name="first_paid_at"]',
    )!;
  expect(zone.value).toBe("");
  expect(first.value).toBe("");
  fireEvent.change(zone, { target: { value: "Asia/Shanghai" } });
  fireEvent.change(first, { target: { value: "2024-01-31T09:00:00+08:00" } });
  fireEvent.change(view.container.querySelector("textarea")!, {
    target: {
      value:
        "Verified original onboarding payment evidence with the tenant owner",
    },
  });
  fireEvent.click(view.getByRole("checkbox"));
  fireEvent.submit(view.container.querySelector("form")!);
  await settle();
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/platform/tenants/1/billing-calendar",
    {
      method: "PUT",
      body: {
        timezone: "Asia/Shanghai",
        first_paid_at: "2024-01-31T09:00:00+08:00",
        reason:
          "Verified original onboarding payment evidence with the tenant owner",
      },
    },
  );
  expect(view.getByText("calendar.admin.readonly")).toBeTruthy();
  expect(view.container.querySelector("form")).toBeNull();
});
it("existing calendar is immutable even when current business timezone differs", async () => {
  state.request.mockResolvedValue({
    tenant_id: 1,
    calendar,
    review_required: false,
    current_business_timezone: "America/New_York",
  });
  const view = render(<BillingCalendarAdmin tenants={tenants} />);
  fireEvent.change(view.getByRole("combobox"), { target: { value: "1" } });
  await settle();
  expect(view.getByText("Asia/Shanghai")).toBeTruthy();
  expect(view.getAllByText(calendar.first_paid_at).length).toBe(2);
  expect(view.container.querySelector("form")).toBeNull();
  expect(
    state.request.mock.calls.some(([, options]) => options?.method === "PUT"),
  ).toBe(false);
});
it("new customers receive an automatic calendar and cannot be manually seeded", async () => {
  state.request.mockResolvedValue({
    tenant_id: 2,
    calendar: null,
    review_required: false,
    current_business_timezone: "Asia/Shanghai",
  });
  const view = render(<BillingCalendarAdmin tenants={tenants} />);
  fireEvent.change(view.getByRole("combobox"), { target: { value: "2" } });
  await settle();
  expect(view.getByText("calendar.admin.automatic")).toBeTruthy();
  expect(view.container.querySelector("form")).toBeNull();
});
it("tenant switch hides the previous tenant's legacy form before showing new state", async () => {
  state.request.mockImplementation((path: string) =>
    Promise.resolve({
      tenant_id: path.includes("/2/") ? 2 : 1,
      calendar: null,
      review_required: !path.includes("/2/"),
      current_business_timezone: "Asia/Shanghai",
    }),
  );
  const view = render(<BillingCalendarAdmin tenants={tenants} />);
  fireEvent.change(view.getByRole("combobox"), { target: { value: "1" } });
  await settle();
  expect(view.container.querySelector("form")).toBeTruthy();
  fireEvent.change(view.getByRole("combobox"), { target: { value: "2" } });
  await settle();
  expect(view.container.querySelector("form")).toBeNull();
  expect(view.getByText("calendar.admin.automatic")).toBeTruthy();
});
