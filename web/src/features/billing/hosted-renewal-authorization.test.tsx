import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
  HostedRenewalAuthorization,
  officialStripeHostedURL,
  type HostedAuthorization,
} from "./hosted-renewal-authorization";
const state = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
const authorization: HostedAuthorization = {
  id: 7,
  merchant_id: 3,
  state: "ready",
  version: 2,
  url: "https://checkout.stripe.com/c/pay/cs_test_fixture#safe",
  currency: "USD",
  quote_digest: "a".repeat(64),
  expires_at: "2099-01-01T00:00:00Z",
  binding_id: null,
  last_error: "",
};
function open(onVerified = vi.fn().mockResolvedValue(undefined)) {
  return {
    view: render(
      <HostedRenewalAuthorization
        merchants={[{ merchant_id: 3, name: "Stripe sandbox" }]}
        quoteDigest={"a".repeat(64)}
        canStart={true}
        onVerified={onVerified}
      />,
    ),
    onVerified,
  };
}
it("separates hosted setup consent, official link, verification and enrollment", async () => {
  state.request.mockImplementation(
    (path: string, options?: { method: string }) =>
      Promise.resolve(
        options?.method === "POST"
          ? {
              authorization: path.endsWith("/complete")
                ? {
                    ...authorization,
                    state: "verified",
                    version: 3,
                    binding_id: 9,
                  }
                : authorization,
            }
          : { authorizations: [] },
      ),
  );
  const { view, onVerified } = open();
  await waitFor(() => expect(state.request).toHaveBeenCalled());
  fireEvent.change(view.getByLabelText("billing_hosted.channel"), {
    target: { value: "3" },
  });
  expect(
    (
      view.getByRole("button", {
        name: "billing_hosted.start",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.click(view.getByRole("checkbox"));
  fireEvent.click(view.getByRole("button", { name: "billing_hosted.start" }));
  await view.findByRole("link", { name: "billing_hosted.open" });
  const start = state.request.mock.calls.find(
    (c) => c[0].endsWith("/authorizations") && c[1]?.method === "POST",
  );
  expect(start?.[1].body).toEqual({
    merchant_id: 3,
    quote_digest: "a".repeat(64),
    confirmed: true,
  });
  expect(start?.[1].idempotencyKey).toBeTruthy();
  expect(onVerified).not.toHaveBeenCalled();
  fireEvent.click(
    view.getByRole("button", { name: "billing_hosted.complete" }),
  );
  await waitFor(() => expect(onVerified).toHaveBeenCalledTimes(1));
  expect(state.request.mock.calls.some((c) => c[0].endsWith("/enroll"))).toBe(
    false,
  );
  expect(
    state.request.mock.calls.find((c) => c[0].endsWith("/7/complete"))?.[1]
      .body,
  ).toEqual({ version: 2 });
});
it("recovers lost creation by GET of the same key without another POST", async () => {
  state.request.mockImplementation(
    (path: string, options?: { method: string }) =>
      options?.method === "POST"
        ? Promise.reject(Error("connection lost"))
        : Promise.resolve({
            authorizations: path.includes("request_key=")
              ? [{ ...authorization, state: "uncertain", url: undefined }]
              : [],
          }),
  );
  const { view } = open();
  await waitFor(() => expect(state.request).toHaveBeenCalled());
  fireEvent.change(view.getByLabelText("billing_hosted.channel"), {
    target: { value: "3" },
  });
  fireEvent.click(view.getByRole("checkbox"));
  fireEvent.click(view.getByRole("button", { name: "billing_hosted.start" }));
  await view.findByRole("button", { name: "billing_hosted.reconcile" });
  const creates = state.request.mock.calls.filter(
    (c) => c[1]?.method === "POST",
  );
  expect(creates).toHaveLength(1);
  expect(
    state.request.mock.calls.some((c) =>
      c[0].endsWith("?request_key=" + creates[0][1].idempotencyKey),
    ),
  ).toBe(true);
  expect(
    (
      view.getByRole("button", {
        name: "billing_hosted.start",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
it("rejects non-official hosted URLs even for ready receipts", async () => {
  for (const value of [
    "javascript:alert(1)",
    "http://checkout.stripe.com/c/x",
    "https://checkout.stripe.com.evil.test/c/x",
    "https://x@checkout.stripe.com/c/x",
    "https://checkout.stripe.com:444/c/x",
    "https://checkout.stripe.com/not-hosted",
  ]) {
    expect(officialStripeHostedURL(value)).toBeNull();
  }
  state.request.mockResolvedValue({
    authorizations: [{ ...authorization, url: "https://evil.test/c/x" }],
  });
  const { view } = open();
  await view.findByRole("alert");
  expect(view.queryByRole("link")).toBeNull();
});
