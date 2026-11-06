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
it("reads quarantine from healthz and does not call registration-status", async () => {
  state.request.mockImplementation(async (path: string) => {
    if (String(path).includes("registration-status")) {
      throw Error("registration must not be called");
    }
    return { quarantined: true };
  });
  const hook = renderHook(() => usePublicStatus());
  expect(hook.result.current.quarantined).toBe(false);
  await settle();
  expect(hook.result.current).toMatchObject({
    quarantined: true,
    loaded: true,
  });
  expect(state.request.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/healthz",
  ]);
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
});
it("stays out of quarantine when healthz is unavailable", async () => {
  state.request.mockImplementation(async () => {
    throw Error("unavailable");
  });
  const hook = renderHook(() => usePublicStatus());
  await settle();
  expect(hook.result.current).toMatchObject({
    quarantined: false,
    loaded: true,
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
