import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { usePublicStatus } from "./public-status";
import { SettingsEditor } from "./settings-editor";
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
async function settle() {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve();
  });
}
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("requires both explicit registration capability and non-quarantined health before enabling", async () => {
  state.request.mockImplementation(async (path: string) =>
    path.includes("registration-status")
      ? { enabled: true }
      : { quarantined: true },
  );
  const hook = renderHook(() => usePublicStatus());
  expect(hook.result.current.registration).toBe(false);
  await settle();
  expect(hook.result.current).toMatchObject({
    registration: false,
    quarantined: true,
    loaded: true,
  });
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
});
it("retains the quarantine banner when registration status is unavailable", async () => {
  state.request.mockImplementation(async (path: string) => {
    if (path.includes("registration-status")) throw Error("unavailable");
    return { quarantined: true };
  });
  const hook = renderHook(() => usePublicStatus());
  await settle();
  expect(hook.result.current).toMatchObject({
    registration: false,
    quarantined: true,
  });
});
it("saves only the explicitly edited IANA business timezone", async () => {
  state.request.mockResolvedValue({
    settings: { business_timezone: "Asia/Shanghai" },
  });
  const view = render(<SettingsEditor />);
  await settle();
  const field = view.getByLabelText(/page.settings.business_timezone/);
  fireEvent.change(field, { target: { value: "America/New_York" } });
  fireEvent.submit(field.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(
    ([, options]) => options?.method === "PATCH",
  );
  expect(call?.[0]).toBe("/api/v1/tenants/current");
  expect(call?.[1].body).toEqual({
    settings: { business_timezone: "America/New_York" },
  });
});
