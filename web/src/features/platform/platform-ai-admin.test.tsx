import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PlatformAIAdmin } from "./platform-ai-admin";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string, params?: unknown) =>
    params ? key + JSON.stringify(params) : key,
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
    for (let i = 0; i < 15; i++) await Promise.resolve();
  });
}
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
function setup(mode = "platform") {
  state.request.mockImplementation(async (path: string) =>
    path.endsWith("configuration")
      ? {
          configured: true,
          configuration: {
            mode,
            provider: "openai",
            endpoint: "https://models.example.test/v1",
            model: "test-model",
            enabled: true,
            credential_configured: true,
            input_micros_per_million: 1000000,
            output_micros_per_million: 2000000,
            cost_currency: "USD",
          },
          tenant_consent: { allowed: false, tenant_local_only: true },
          cloud_endpoints: ["https://models.example.test/v1"],
        }
      : {
          requests: [
            {
              id: 42,
              mode,
              provider: "openai",
              model: "test-model",
              status: "uncertain",
              reserved_tokens: 100,
              reserved_cost_micros: 500,
              cost_currency: "USD",
            },
          ],
        },
  );
  const view = render(
    <PlatformAIAdmin tenants={[{ id: 7, name: "Tenant Seven" }]} />,
  );
  fireEvent.change(view.getByLabelText("ai.admin.tenant"), {
    target: { value: "7" },
  });
  return view;
}
it("reads metadata only and cannot grant tenant consent or auto-probe a cloud target", async () => {
  const view = setup();
  await settle();
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
  expect(view.getByRole("button", { name: "ai.admin.health" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    (view.getByLabelText("ai.admin.api_key") as HTMLInputElement).value,
  ).toBe("");
  expect(view.getByText("ai.admin.local_only")).toBeTruthy();
});
it("saves explicit model prices without copying credentials or tenant consent", async () => {
  const view = setup();
  await settle();
  const field = view.getByLabelText("ai.admin.api_key");
  fireEvent.submit(field.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(
    ([, options]) => options?.method === "POST",
  );
  expect(call?.[0]).toBe("/api/v1/platform/tenants/7/ai/configuration");
  expect(call?.[1].body).toMatchObject({
    api_key: "",
    cost_currency: "USD",
    input_micros_per_million: 1000000,
    output_micros_per_million: 2000000,
  });
  expect(call?.[1].body).not.toHaveProperty("tenant_consent");
  expect(call?.[1].body).not.toHaveProperty("local_only");
});
it("requires explicit actual facts and rejects platform model cost for BYO", async () => {
  const view = setup("byo");
  await settle();
  fireEvent.click(view.getByRole("button", { name: "ai.admin.reconcile" }));
  const reason = view.getByLabelText("ai.admin.reason");
  fireEvent.change(reason, {
    target: { value: "Provider confirmed request was never consumed" },
  });
  fireEvent.change(view.getByLabelText("ai.admin.decision"), {
    target: { value: "released" },
  });
  fireEvent.change(view.getByLabelText("ai.admin.actual_tokens"), {
    target: { value: "0" },
  });
  fireEvent.change(view.getByLabelText("ai.admin.actual_cost"), {
    target: { value: "1" },
  });
  fireEvent.submit(reason.closest("form")!);
  await settle();
  expect(
    state.request.mock.calls.some(([, options]) => options?.method === "POST"),
  ).toBe(false);
  fireEvent.change(view.getByLabelText("ai.admin.actual_cost"), {
    target: { value: "0" },
  });
  fireEvent.submit(reason.closest("form")!);
  await settle();
  const call = state.request.mock.calls.find(
    ([, options]) => options?.method === "POST",
  );
  expect(call?.[0]).toBe("/api/v1/platform/tenants/7/ai/usage/42/resolve");
  expect(call?.[1].body).toMatchObject({
    status: "released",
    tokens: 0,
    cost_micros: 0,
    cost_currency: "USD",
  });
});

it("keeps exact observed token strings instead of rounding unknown provider usage", async () => {
  const view = setup();
  state.request.mockImplementation(async (path: string) =>
    path.endsWith("configuration")
      ? {
          configured: false,
          configuration: null,
          tenant_consent: { allowed: false, tenant_local_only: true },
        }
      : {
          requests: [
            {
              id: 88,
              mode: "platform",
              provider: "openai",
              model: "observed",
              status: "uncertain",
              reserved_tokens: 100,
              reserved_cost_micros: 500,
              cost_currency: "USD",
              actual_tokens: null,
              actual_tokens_exact: "9007199254740993",
            },
          ],
        },
  );
  fireEvent.change(view.getByLabelText("ai.admin.tenant"), {
    target: { value: "" },
  });
  fireEvent.change(view.getByLabelText("ai.admin.tenant"), {
    target: { value: "7" },
  });
  await settle();
  expect(view.getByText(/9007199254740993/)).toBeTruthy();
});
