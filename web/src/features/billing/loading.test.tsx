import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BillingPage } from "./billing-page";
import { AuditPage } from "../audit/audit-page";
import { PlatformPage } from "../platform/platform-page";

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
  vi.restoreAllMocks();
  window.history.replaceState({}, "", "/");
});

describe("checkout return is informational", () => {
  for (const [query, key] of [
    ["paid=1", "page.billing.checkout_returned"],
    ["canceled=1", "page.billing.checkout_canceled"],
    ["paid=1&canceled=1", "page.billing.checkout_canceled"],
  ]) {
    it(`${query} never settles or creates an order`, async () => {
      window.history.replaceState({}, "", `/billing?${query}`);
      state.request.mockResolvedValue({
        subscription: null,
        plan: null,
        invoices: [],
        plans: [],
      });
      const view = render(<BillingPage />);
      await act(async () => {
        for (let i = 0; i < 12; i++) await Promise.resolve();
      });
      expect(view.getByRole("status").textContent).toBe(key);
      expect(state.request).toHaveBeenCalledTimes(3);
      for (const [, options] of state.request.mock.calls) {
        expect(options.method ?? "GET").toBe("GET");
      }
    });
  }
});

it("editing a disabled brand preserves its disabled status", async () => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("settings")
        ? { settings: {} }
        : {
            brands: [
              {
                id: 9,
                slug: "disabled-brand",
                name: "Disabled brand",
                status: "disabled",
              },
            ],
            tenants: [],
            plans: [],
            coupons: [],
            logs: [],
            hosts: [],
            overview: {
              tenants: 0,
              active_tenants: 0,
              users: 0,
              domains: 0,
              mailboxes: 0,
              active_subscriptions: 0,
            },
          },
    ),
  );
  const view = render(<PlatformPage />);
  await act(async () => {
    for (let i = 0; i < 12; i++) await Promise.resolve();
  });
  fireEvent.click(view.getByRole("button", { name: "action.edit" }));
  await act(async () => {
    fireEvent.click(
      view.getByRole("button", { name: "page.platform.save_brand" }),
    );
  });
  const save = state.request.mock.calls.find(
    ([path, options]) =>
      path === "/api/v1/platform/brands" && options?.method === "POST",
  );
  expect(save?.[1].body).toMatchObject({
    slug: "disabled-brand",
    status: "disabled",
  });
});

describe("management loading remains bounded", () => {
  for (const [name, Component, count] of [
    ["billing", BillingPage, 3],
    ["audit", AuditPage, 1],
    ["platform", PlatformPage, 8],
  ] as const) {
    it(`${name} loads once and stays idle after rendering response objects`, async () => {
      state.request.mockImplementation((path: string) =>
        Promise.resolve(
          path.endsWith("settings")
            ? { settings: {} }
            : {
                subscription: null,
                plan: null,
                invoices: [],
                plans: [],
                logs: [],
                brands: [],
                coupons: [],
                tenants: [],
                overview: {
                  tenants: 0,
                  active_tenants: 0,
                  users: 0,
                  domains: 0,
                  mailboxes: 0,
                  active_subscriptions: 0,
                },
              },
        ),
      );
      const view = render(<Component />);
      await act(async () => {
        for (let i = 0; i < 12; i++) await Promise.resolve();
      });
      expect(state.request).toHaveBeenCalledTimes(count);
      view.rerender(<Component />);
      await act(async () => {
        for (let i = 0; i < 12; i++) await Promise.resolve();
      });
      expect(state.request).toHaveBeenCalledTimes(count);
    });
  }
});
