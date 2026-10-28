import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { BrandHostLifecycle } from "./brand-host-lifecycle";
const api = vi.hoisted(() => vi.fn());
const errorMessage = () => "lifecycle-error";
vi.mock("@/lib/api/client", () => ({ request: api }));
vi.mock("@/lib/api/use-api-error-message", () => ({
  useApiErrorMessage: () => errorMessage,
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(() => {
  cleanup();
  api.mockReset();
});
it("requires confirmation and never accepts a user-supplied proof token", async () => {
  api.mockResolvedValue({
    host: { status: "pending-verify" },
    certificate: null,
  });
  render(
    <BrandHostLifecycle hostname="brand.example.test" onChange={() => {}} />,
  );
  const button = await screen.findByRole("button", {
    name: "brand.host.verify",
  });
  expect((button as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(button);
  await waitFor(() =>
    expect(api).toHaveBeenCalledWith(
      "/api/v1/platform/brand-hosts/brand.example.test/lifecycle",
      { method: "POST", body: { action: "verify", confirm: true } },
    ),
  );
  expect(screen.queryByRole("textbox")).toBeNull();
});
it("refreshes the actual verdict and does not mark an untrusted active certificate ready", async () => {
  api
    .mockResolvedValueOnce({ host: { status: "verified" }, certificate: null })
    .mockResolvedValue({
      host: { status: "active" },
      certificate: { trusted: false },
    });
  render(
    <BrandHostLifecycle hostname="brand.example.test" onChange={() => {}} />,
  );
  await screen.findByText(/phase_verified/);
  fireEvent.click(screen.getByRole("button", { name: "brand.host.refresh" }));
  await screen.findByText(/brand.host.not_ready/);
  expect(screen.queryByText("brand.host.trusted")).toBeNull();
});
