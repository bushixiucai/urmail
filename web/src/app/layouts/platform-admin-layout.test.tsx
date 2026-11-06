import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { PlatformAdminLayout } from "./platform-admin-layout";
import { setAssumedTenant } from "@/lib/assumed-tenant";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) =>
      options?.defaultValue || key,
    i18n: { exists: () => true },
  }),
}));

vi.mock("@/app/providers", () => ({
  useI18nLocale: () => ({ locale: "zh-CN", setLocale: vi.fn() }),
}));

const mockLogout = vi.fn();
vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({
    user: {
      id: 1,
      email: "superadmin@urmail.cn",
      display_name: "Super Admin",
      locale: "zh-CN",
    },
    tenants: [],
    currentTenant: null,
    isAdmin: true,
    logout: mockLogout,
  }),
}));

describe("PlatformAdminLayout", () => {
  beforeEach(() => {
    sessionStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    cleanup();
  });

  it("renders system navigation without commercial items", () => {
    render(
      <MemoryRouter initialEntries={["/platform"]}>
        <PlatformAdminLayout>
          <div>Platform Content</div>
        </PlatformAdminLayout>
      </MemoryRouter>,
    );

    expect(screen.getAllByText("平台概览").length).toBeGreaterThan(0);
    expect(screen.getAllByText("租户管理").length).toBeGreaterThan(0);
    expect(screen.queryByText("套餐与计划")).toBeNull();
    expect(screen.queryByText("财务与结算")).toBeNull();
    expect(screen.getAllByText("品牌与主机").length).toBeGreaterThan(0);
    expect(screen.getAllByText("AI 与系统配置").length).toBeGreaterThan(0);
    expect(screen.getAllByText("平台工单").length).toBeGreaterThan(0);

    // Tenant-specific sidebar items must NOT exist in platform admin layout
    expect(screen.queryByText("layout.mailboxes")).toBeNull();
    expect(screen.queryByText("layout.aliases")).toBeNull();
    expect(screen.queryByText("layout.select_tenant")).toBeNull();
  });

  it("displays prominent yellow warning banner when in assume tenant mode", () => {
    setAssumedTenant({ id: 99, name: "Target Tenant", slug: "target" });

    render(
      <MemoryRouter initialEntries={["/platform"]}>
        <PlatformAdminLayout>
          <div>Platform Content</div>
        </PlatformAdminLayout>
      </MemoryRouter>,
    );

    const banner = screen.getByRole("alert");
    expect(banner).not.toBeNull();
    expect(banner.textContent).toContain("Target Tenant");
    expect(banner.textContent).toContain("target");

    const exitBtn = screen.getByRole("button", {
      name: "退出视察返回平台",
    });
    expect(exitBtn).not.toBeNull();

    fireEvent.click(exitBtn);
    expect(sessionStorage.getItem("urmail_assumed_tenant")).toBeNull();
  });
});
