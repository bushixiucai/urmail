import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { CloudConsent, type CloudPolicy } from "./cloud-consent";
const { request, t } = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string) => key,
}));
vi.mock("@/lib/api/client", () => ({ request }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t }) }));
afterEach(() => {
  cleanup();
  request.mockReset();
});
const policy: CloudPolicy = {
  allowed: false,
  provider: "openai",
  model: "target-one",
  endpoint: "https://approved.example.test/v1",
  configuration_digest: "a".repeat(64),
  scope_hash: "b".repeat(64),
  requires_confirmation: true,
  tenant_local_only: true,
};
it("requires explicit authorization for the exact target and resets confirmation on target changes", async () => {
  request.mockResolvedValue(undefined);
  const reload = vi.fn();
  const view = render(<CloudConsent policy={policy} reload={reload} />);
  const authorize = screen.getByRole("button", {
    name: "page.ai.consent_authorize",
  }) as HTMLButtonElement;
  expect(authorize.disabled).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  expect(authorize.disabled).toBe(false);
  view.rerender(
    <CloudConsent
      policy={{
        ...policy,
        model: "target-two",
        configuration_digest: "c".repeat(64),
      }}
      reload={reload}
    />,
  );
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
    false,
  );
  expect(authorize.disabled).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(authorize);
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith("/api/v1/ai/consent", {
      method: "POST",
      body: {
        configuration_digest: "c".repeat(64),
        scope_hash: "b".repeat(64),
        allow_cloud: true,
        confirmed: true,
      },
    }),
  );
  expect(reload).toHaveBeenCalledTimes(1);
});
it("revokes consent and enforces local-only without requiring another positive confirmation", async () => {
  request.mockResolvedValue(undefined);
  render(
    <CloudConsent
      policy={{ ...policy, allowed: true, tenant_local_only: false }}
      reload={() => {}}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.ai.consent_revoke" }),
  );
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/ai/consent",
      expect.objectContaining({
        body: expect.objectContaining({ allow_cloud: false, confirmed: false }),
      }),
    ),
  );
});
