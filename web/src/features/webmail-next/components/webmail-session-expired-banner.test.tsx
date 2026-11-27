import {
  render,
  screen,
  fireEvent,
  cleanup,
  act,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { WebmailSessionExpiredBanner } from "./webmail-session-expired-banner";
import { listFolders, setMailboxSession } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  window.localStorage.clear();
});

describe("WebmailSessionExpiredBanner", () => {
  it("renders nothing while the session is alive", () => {
    render(
      <MemoryRouter initialEntries={["/webmail/inbox"]}>
        <Routes>
          <Route path="*" element={<WebmailSessionExpiredBanner />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.queryByTestId("session-expired-banner")).toBeNull();
  });

  it("appears on the 401 funnel broadcast and re-login carries ?next=", async () => {
    setMailboxSession({
      token: "dying-token",
      address: "user@example.test",
      expires_at: new Date(Date.now() + 60_000).toISOString(),
    });
    const fetcher = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      json: async () => ({
        error: {
          code: "MAILBOX_UNAUTHORIZED",
          message: "error.MAILBOX_UNAUTHORIZED",
        },
      }),
    });
    vi.stubGlobal("fetch", fetcher);

    render(
      <MemoryRouter initialEntries={["/webmail/inbox"]}>
        <Routes>
          <Route
            path="/webmail/login"
            element={<div data-testid="login-landing">login</div>}
          />
          <Route path="*" element={<WebmailSessionExpiredBanner />} />
        </Routes>
      </MemoryRouter>,
    );

    // The request-level funnel declares the session dead and broadcasts.
    await act(async () => {
      await expect(listFolders()).rejects.toMatchObject({ status: 401 });
    });
    expect(screen.getByTestId("session-expired-banner")).toBeTruthy();
    expect(screen.queryByTestId("login-landing")).toBeNull();

    fireEvent.click(screen.getByTestId("session-expired-relogin"));
    expect(screen.getByTestId("login-landing")).toBeTruthy();
  });
});
