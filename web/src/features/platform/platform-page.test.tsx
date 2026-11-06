import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PlatformPage } from "./platform-page";
import { getAssumedTenant } from "@/lib/assumed-tenant";

const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string, options?: { defaultValue?: string }) =>
    options?.defaultValue || key,
  i18n: { exists: () => true },
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));

vi.mock("@/app/providers", () => ({
  useI18nLocale: () => ({ locale: "zh-CN", setLocale: vi.fn() }),
}));

vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({
    user: {
      id: 1,
      email: "admin@urmail.cn",
      display_name: "Admin",
      locale: "zh-CN",
    },
    tenants: [{ id: 101, name: "Alpha Tenant", slug: "alpha" }],
    currentTenant: null,
    isAdmin: true,
    switchTenant: vi.fn(),
  }),
}));

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

describe("PlatformPage with Tabs", () => {
  beforeEach(() => {
    sessionStorage.clear();
    state.request.mockImplementation((path: string) => {
      const baseResponse = {
        days: [7, 3, 1, 0],
        alerts: [],
        merchants: [],
        reviews: [],
        refunds: [],
        hosts: [],
        tenants: [
          {
            id: 101,
            name: "Alpha Tenant",
            slug: "alpha",
            status: "active",
            domain_count: 2,
            mailbox_count: 5,
          },
          {
            id: 102,
            name: "Beta Org",
            slug: "beta",
            status: "suspended",
            domain_count: 1,
            mailbox_count: 3,
          },
        ],
        brands: [
          { id: 1, slug: "default", name: "Default Brand", status: "active" },
        ],
        plans: [
          {
            code: "pro",
            name: { en: "Pro", "zh-CN": "专业版" },
            price_cents: 9900,
            currency: "CNY",
            quota_bytes: 10737418240,
            mailbox_limit: 20,
            domain_limit: 5,
            daily_send_limit: 500,
            active: true,
          },
        ],
        coupons: [{ code: "PROMO2026" }],
        logs: [],
        overview: {
          tenants: 2,
          active_tenants: 2,
          users: 10,
          domains: 3,
          mailboxes: 8,
          active_subscriptions: 2,
        },
        profile: {
          party: {},
          tax: {
            configured: false,
            rate_basis_points: 0,
            included_in_total: true,
          },
          locale: "en",
        },
        settings: {},
        channel: null,
      };

      if (path.endsWith("settings")) {
        return Promise.resolve({ settings: {} });
      }
      return Promise.resolve(baseResponse);
    });
  });

  it("renders tenant, brand, and AI tabs without commercial tabs", async () => {
    render(<PlatformPage />);
    await act(async () => {
      for (let i = 0; i < 12; i++) await Promise.resolve();
    });

    const tabNav = screen.getByRole("navigation", { name: "Platform Tabs" });
    expect(within(tabNav).getByText("平台概览")).not.toBeNull();
    expect(within(tabNav).getByText("租户管理")).not.toBeNull();
    expect(within(tabNav).queryByText("套餐计划")).toBeNull();
    expect(within(tabNav).queryByText("商业化与财务")).toBeNull();
    expect(within(tabNav).getByText("品牌与主机")).not.toBeNull();
    expect(within(tabNav).getByText("AI与系统规则")).not.toBeNull();
    expect(within(tabNav).getByText("异地备份")).not.toBeNull();
    expect(
      state.request.mock.calls.some(([path]) =>
        String(path).includes("/platform/plans"),
      ),
    ).toBe(false);
    expect(
      state.request.mock.calls.some(([path]) =>
        String(path).includes("/platform/coupons"),
      ),
    ).toBe(false);
  });

  it("filters tenants by search query in Tab 2", async () => {
    render(<PlatformPage defaultTab="tenants" />);
    await act(async () => {
      for (let i = 0; i < 12; i++) await Promise.resolve();
    });

    const table = screen.getByRole("table");
    expect(within(table).getByText("Alpha Tenant")).not.toBeNull();
    expect(within(table).getByText("Beta Org")).not.toBeNull();

    const searchInput = screen.getByPlaceholderText("搜索租户名称或标识...");
    fireEvent.change(searchInput, { target: { value: "alpha" } });

    expect(within(table).getByText("Alpha Tenant")).not.toBeNull();
    expect(within(table).queryByText("Beta Org")).toBeNull();
  });

  it("triggers assume tenant when clicking 视察", async () => {
    render(<PlatformPage defaultTab="tenants" />);
    await act(async () => {
      for (let i = 0; i < 12; i++) await Promise.resolve();
    });

    const assumeBtns = screen.getAllByRole("button", { name: "视察" });
    expect(assumeBtns.length).toBeGreaterThan(0);

    fireEvent.click(assumeBtns[0]);
    const assumed = getAssumedTenant();
    expect(assumed).toEqual({
      id: 101,
      name: "Alpha Tenant",
      slug: "alpha",
    });
  });
});
