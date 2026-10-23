import { act, cleanup, render } from "@testing-library/react";
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
