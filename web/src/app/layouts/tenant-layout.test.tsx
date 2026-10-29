import { render, screen, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { TenantLayout } from "./tenant-layout";

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

vi.mock("@/features/webmail/use-mailbox-native", () => ({
  useMailboxNative: () => ({ native: true, loaded: true }),
}));

vi.mock("@/lib/api/client", () => ({
  request: vi.fn().mockResolvedValue({}),
}));

const mockLogout = vi.fn();
vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({
    user: {
      id: 1,
      email: "tenant@urmail.cn",
      display_name: "Tenant Admin",
      locale: "zh-CN",
    },
    tenants: [
      {
        id: 1,
        name: "Acme Corp",
        slug: "acme",
        status: "active",
        role: "admin",
      },
    ],
    currentTenant: {
      id: 1,
      name: "Acme Corp",
      slug: "acme",
      status: "active",
      role: "admin",
    },
    isAdmin: true,
    logout: mockLogout,
    switchTenant: vi.fn(),
  }),
}));

describe("TenantLayout Navigation Groups & Icons", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    cleanup();
  });

  it("renders structured navigation groups with distinct items", () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <TenantLayout>
          <div>Dashboard Body</div>
        </TenantLayout>
      </MemoryRouter>,
    );

    // Group titles
    expect(screen.getAllByText("业务总览").length).toBeGreaterThan(0);
    expect(screen.getAllByText("邮件资产").length).toBeGreaterThan(0);
    expect(screen.getAllByText("合规与外发").length).toBeGreaterThan(0);
    expect(screen.getAllByText("增值能力").length).toBeGreaterThan(0);
    expect(screen.getAllByText("组织与账单").length).toBeGreaterThan(0);

    // Navigation items
    expect(screen.getAllByText("layout.dashboard").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.webmail").length).toBeGreaterThan(0);
    expect(screen.getAllByText("layout.domains").length).toBeGreaterThan(0);
    expect(screen.getAllByText("layout.mailboxes").length).toBeGreaterThan(0);
    expect(screen.getAllByText("layout.aliases").length).toBeGreaterThan(0);
    expect(screen.getAllByText("archive.title").length).toBeGreaterThan(0);
    expect(screen.getAllByText("delivery.title").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.marketing").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.audit").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.ai").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.notifications").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.support").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.billing").length).toBeGreaterThan(0);
    expect(screen.getAllByText("layout.settings").length).toBeGreaterThan(0);
    expect(screen.getAllByText("nav.platform").length).toBeGreaterThan(0);
  });
});
