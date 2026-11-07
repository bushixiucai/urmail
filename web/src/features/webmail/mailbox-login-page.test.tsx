import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { MailboxLoginPage } from "./mailbox-login-page";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("./use-mailbox-native", () => ({
  useMailboxNative: () => ({ native: true, loaded: true }),
}));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe("mailbox login", () => {
  it("posts email+password to mailbox session and stores the bearer", async () => {
    const fetcher = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        token: "mbox-token",
        expires_at: "2099-01-01T00:00:00Z",
        address: "user@example.test",
      }),
    });
    vi.stubGlobal("fetch", fetcher);

    render(
      <MemoryRouter>
        <MailboxLoginPage />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByLabelText("page.webmail.field_email"), {
      target: { value: "user@example.test" },
    });
    fireEvent.change(screen.getByLabelText("page.webmail.field_password"), {
      target: { value: "secret-pass" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.login_submit" }),
    );

    await waitFor(() =>
      expect(fetcher).toHaveBeenCalledWith(
        "/api/v1/mailbox/session",
        expect.objectContaining({
          method: "POST",
          credentials: "same-origin",
        }),
      ),
    );
    const body = JSON.parse(fetcher.mock.calls[0][1].body as string);
    expect(body).toEqual({
      email: "user@example.test",
      password: "secret-pass",
      remember: false,
    });
    expect(window.sessionStorage.getItem("urmail.mailbox-token")).toBe(
      "mbox-token",
    );
    expect(window.sessionStorage.getItem("urmail.mailbox-address")).toBe(
      "user@example.test",
    );
  });

  it("renders switch to admin link and remembers login when checkbox checked", async () => {
    const fetcher = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        token: "mbox-token-remembered",
        expires_at: "2099-01-01T00:00:00Z",
        address: "user@example.test",
      }),
    });
    vi.stubGlobal("fetch", fetcher);

    render(
      <MemoryRouter>
        <MailboxLoginPage />
      </MemoryRouter>,
    );

    const adminLink = screen.getByRole("link", {
      name: "page.webmail.switch_to_admin",
    });
    expect(adminLink.getAttribute("href")).toBe("/login");

    const rememberCheck = screen.getByLabelText("login.remember_me");
    expect(rememberCheck).not.toBeNull();
    fireEvent.click(rememberCheck);

    fireEvent.change(screen.getByLabelText("page.webmail.field_email"), {
      target: { value: "user@example.test" },
    });
    fireEvent.change(screen.getByLabelText("page.webmail.field_password"), {
      target: { value: "secret-pass" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.login_submit" }),
    );

    await waitFor(() => {
      expect(window.localStorage.getItem("urmail.mailbox-token")).toBe(
        "mbox-token-remembered",
      );
    });
    // Remember also reaches the API so the backend can extend the TTL.
    const body = JSON.parse(fetcher.mock.calls[0][1].body as string);
    expect(body).toMatchObject({ remember: true });
  });
});
