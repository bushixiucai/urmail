import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { LoginPage } from "./login-page";

const mockLogin = vi.fn();

vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({
    login: mockLogin,
    isAuthenticated: false,
    isLoading: false,
  }),
}));

vi.mock("@/app/providers", () => ({
  useI18nLocale: () => ({
    locale: "zh-CN",
    setLocale: vi.fn(),
  }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  mockLogin.mockReset();
});

describe("LoginPage", () => {
  it("renders remember me checkbox and employee webmail link", async () => {
    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );

    const webmailLink = screen.getByRole("link", {
      name: "login.switch_to_webmail",
    });
    expect(webmailLink.getAttribute("href")).toBe("/webmail/login");

    const rememberCheck = screen.getByLabelText(
      "login.remember_me",
    ) as HTMLInputElement;
    expect(rememberCheck).not.toBeNull();
    expect(rememberCheck.checked).toBe(true);
  });

  it("submits login with remember option and saves credentials", async () => {
    const fetcher = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        token: "admin-jwt-token",
        csrf_token: "admin-csrf-token",
        user: {
          id: 1,
          email: "admin@example.com",
          display_name: "Admin",
          locale: "zh-CN",
        },
        tenants: [
          {
            id: 10,
            name: "Corp",
            slug: "corp",
            status: "active",
            role: "owner",
          },
        ],
        is_admin: true,
      }),
    });
    vi.stubGlobal("fetch", fetcher);

    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByPlaceholderText("admin@example.com"), {
      target: { value: "admin@example.com" },
    });
    fireEvent.change(screen.getByPlaceholderText("••••••••"), {
      target: { value: "SuperPassword123!" },
    });
    fireEvent.click(screen.getByRole("button", { name: "login.submit" }));

    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith(
        "admin-jwt-token",
        "admin-csrf-token",
        expect.objectContaining({ email: "admin@example.com" }),
        expect.arrayContaining([expect.objectContaining({ slug: "corp" })]),
        true,
        undefined,
        true, // remember
      );
    });
  });

  // QA qa-def-console-gate（pm-brand-batch1-add-1）：slice-1 平台门对
  // /auth/login 一律 503 FEATURE_UNAVAILABLE（凭据校验之前 fail-closed）。
  // 登录页必须给「控制台未开放」专用文案＋员工邮箱入口引导，不得显示
  // 泛化的 LOGIN_FAILED 凭据错误。
  it("shows the console-gate notice with a webmail pointer on FEATURE_UNAVAILABLE", async () => {
    const fetcher = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({
        error: {
          code: "FEATURE_UNAVAILABLE",
          message: "error.FEATURE_UNAVAILABLE",
        },
      }),
    });
    vi.stubGlobal("fetch", fetcher);

    render(
      <MemoryRouter>
        <LoginPage />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByPlaceholderText("admin@example.com"), {
      target: { value: "admin@example.com" },
    });
    fireEvent.change(screen.getByPlaceholderText("••••••••"), {
      target: { value: "SuperPassword123!" },
    });
    fireEvent.click(screen.getByRole("button", { name: "login.submit" }));

    const notice = await screen.findByTestId("console-gate-notice");
    expect(notice.textContent).toContain("login.console_gate_notice");
    const webmailLink = within(notice).getByRole("link", {
      name: "login.switch_to_webmail",
    });
    expect(webmailLink.getAttribute("href")).toBe("/webmail/login");
    expect(screen.queryByText("error.LOGIN_FAILED")).toBeNull();
    expect(mockLogin).not.toHaveBeenCalled();
  });
});
