import { act, cleanup, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { NotificationsPage } from "./notifications-page";
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
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("renders scoped localized facts and distinguishes pending email from delivered in-app notices", async () => {
  state.request.mockResolvedValue({
    notifications: [
      {
        id: 1,
        message_key: "notification.subscription.expiring",
        params: { days: 3 },
        channel: "email",
        state: "pending",
        created_at: "2026-10-24T00:00:00Z",
      },
    ],
  });
  const view = render(<NotificationsPage />);
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve();
  });
  expect(view.getByText("notification.subscription.expiring")).toBeTruthy();
  expect(view.getByText(/notify.state.pending/)).toBeTruthy();
  expect(state.request.mock.calls).toHaveLength(1);
  expect(state.request.mock.calls[0][0]).toBe("/api/v1/notifications");
  expect(state.request.mock.calls[0][1]).not.toHaveProperty("method");
});
