import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PaymentMerchantAdmin } from "./payment-merchant-admin";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (k: string) => k,
  i18n: { exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
const settle = async () => {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve();
  });
};
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("keeps optional modules disabled and only offers configured currency capabilities", async () => {
  state.request.mockResolvedValue({ merchants: [] });
  const view = render(<PaymentMerchantAdmin />);
  await settle();
  fireEvent.change(view.getByLabelText("payment.config.provider"), {
    target: { value: "aggregated" },
  });
  expect(view.getByLabelText("payment.config.enable")).toHaveProperty(
    "disabled",
    true,
  );
  expect(view.getByText("payment.config.optional_unavailable")).toBeTruthy();
  fireEvent.change(view.getByLabelText("payment.config.provider"), {
    target: { value: "wechat" },
  });
  expect(view.getByLabelText("CNY")).toBeTruthy();
  expect(view.queryByLabelText("USD")).toBeNull();
  expect(state.request.mock.calls.some(([, o]) => o?.method === "POST")).toBe(
    false,
  );
});
it("edits safe metadata without exposing or replacing existing credentials", async () => {
  state.request.mockResolvedValue({
    merchants: [
      {
        id: 2,
        provider: "stripe",
        environment: "sandbox",
        account_id: "acct_one",
        name: "Stripe",
        enabled: false,
        currencies: ["CNY"],
        entrypoint: "hosted",
        ready: true,
        adapter_available: true,
      },
    ],
  });
  const view = render(<PaymentMerchantAdmin />);
  await settle();
  fireEvent.click(view.getByRole("button", { name: "payment.config.edit" }));
  expect(view.getByLabelText("payment.config.account")).toHaveProperty(
    "readOnly",
    true,
  );
  expect(
    (view.getByLabelText("payment.credentials.secret_key") as HTMLInputElement)
      .value,
  ).toBe("");
  fireEvent.submit(
    view.getByRole("button", { name: "payment.config.save" }).closest("form")!,
  );
  await settle();
  const payload = state.request.mock.calls.find(
    ([, o]) => o?.method === "PATCH",
  );
  expect(payload?.[0]).toBe("/api/v1/platform/payment-merchants/2");
  expect(payload?.[1].body).not.toHaveProperty("credentials");
  expect(payload?.[1].body).toMatchObject({
    account_id: "acct_one",
    environment: "sandbox",
    enabled: false,
  });
});

it("keeps unknown limits blank and rejects incomplete contract evidence before saving", async () => {
  state.request.mockResolvedValue({ merchants: [] });
  const view = render(<PaymentMerchantAdmin />);
  await settle();
  const minimum = view.getByLabelText(
    "CNY · payment.config.min_cents",
  ) as HTMLInputElement;
  expect(minimum.value).toBe("");
  fireEvent.click(view.getByLabelText("CNY"));
  fireEvent.change(minimum, { target: { value: "1" } });
  fireEvent.submit(minimum.closest("form")!);
  await settle();
  expect(view.getByRole("status").textContent).toBe(
    "payment.config.invalid_limits",
  );
  expect(
    state.request.mock.calls.some(([, options]) => options?.method === "POST"),
  ).toBe(false);
});
