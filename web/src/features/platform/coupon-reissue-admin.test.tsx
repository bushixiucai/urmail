import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { CouponReissueAdmin } from "./coupon-reissue-admin";
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
it("only explicitly requests a same-tenant audited replacement without accepting a new value", async () => {
  state.request.mockResolvedValue({ coupons: [] });
  const view = render(<CouponReissueAdmin />);
  await settle();
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
  fireEvent.change(view.getByLabelText("coupon.reissue.tenant"), {
    target: { value: "9" },
  });
  const source = view.getByLabelText("coupon.reissue.source");
  fireEvent.change(source, { target: { value: "LOST-TRIAL" } });
  fireEvent.change(view.getByLabelText("coupon.reissue.reason"), {
    target: { value: "Verified legacy redemption delivered no rights" },
  });
  fireEvent.change(view.getByLabelText("coupon.reissue.until"), {
    target: { value: "2099-01-01T12:00" },
  });
  fireEvent.submit(source.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(
    ([, options]) => options?.method === "POST",
  );
  expect(call?.[0]).toBe("/api/v1/platform/tenants/9/coupon-reissues");
  expect(call?.[1].body).toMatchObject({
    source_code: "LOST-TRIAL",
    reason: "Verified legacy redemption delivered no rights",
  });
  expect(call?.[1].body).not.toHaveProperty("value");
  expect(call?.[1].body).not.toHaveProperty("entitlement_action");
});
