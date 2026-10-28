import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SupportDiagnostics } from "./support-diagnostics";
const api = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/client", () => ({ request: api }));
vi.mock("@/lib/api/use-api-error-message", () => ({
  useApiErrorMessage: () => () => "failure",
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
it("requires collection consent and a separate reviewed attachment choice", async () => {
  const receipt = vi.fn();
  api.mockResolvedValue({
    receipt: "opaque-proof",
    expires_at: "2099-01-01",
    bundle: { fields: [{ name: "mailboxes.total", value: "2" }] },
  });
  render(<SupportDiagnostics disabled={false} onReceipt={receipt} />);
  expect((screen.getByRole("button") as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button"));
  await waitFor(() => expect(screen.getByText("mailboxes.total")).toBeTruthy());
  expect(api).toHaveBeenCalledWith("/api/v1/support/diagnostics", {
    method: "POST",
    body: { consent: true },
  });
  expect(receipt).not.toHaveBeenCalledWith("opaque-proof");
  fireEvent.click(screen.getByLabelText("support.diagnostics_attach"));
  expect(receipt).toHaveBeenLastCalledWith("opaque-proof");
  fireEvent.click(screen.getByLabelText("support.diagnostics_consent"));
  expect(receipt).toHaveBeenLastCalledWith("");
  expect(screen.queryByText("mailboxes.total")).toBeNull();
});
