import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SupportPage } from "./support-page";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (k: string) => k,
  i18n: { exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/app/providers", () => ({ useI18nLocale: () => ({ locale: "en" }) }));
vi.mock("@/app/auth-context", () => ({ useAuth: () => ({ isAdmin: false }) }));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
const settle = async () => {
  await act(async () => {
    for (let i = 0; i < 12; i++) await Promise.resolve();
  });
};
beforeEach(() =>
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("/tickets")
        ? {
            tickets: [{ id: 3, tenant_id: 8, subject: "Help", status: "open" }],
            has_more: false,
          }
        : path.endsWith("/announcements")
          ? {
              announcements: [
                {
                  id: 1,
                  title: { en: "Title" },
                  body: { en: "<img src=x onerror=alert(1)>" },
                },
              ],
            }
          : path.endsWith("/help")
            ? { topics: [] }
            : {
                ticket: {
                  id: 3,
                  tenant_id: 8,
                  subject: "Help",
                  status: "open",
                },
                messages: [
                  { id: 5, body: "<script>unsafe()</script>", is_staff: false },
                ],
                attachments: [],
                has_more: false,
              },
    ),
  ),
);
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("renders ticket and announcement markup as inert plain text", async () => {
  const view = render(<SupportPage />);
  await settle();
  expect(view.getByText("<img src=x onerror=alert(1)>")).toBeTruthy();
  expect(document.querySelector("img")).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "support.open" }));
  await settle();
  expect(view.getByText("<script>unsafe()</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBeNull();
  expect(state.request.mock.calls.some(([, o]) => o?.method === "POST")).toBe(
    false,
  );
  expect(view.queryByText("support.assign")).toBeNull();
});
it("keeps draft or brand filtering on the server boundary", async () => {
  const view = render(<SupportPage />);
  await settle();
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/support/announcements",
    expect.objectContaining({ signal: expect.any(AbortSignal) }),
  );
  expect(view.queryByText("support.announcement.create")).toBeNull();
});
