import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DocumentProfile } from "./document-profile";
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
it("omits untouched masked fields and explicitly clears an edited private field", async () => {
  request.mockImplementation(
    async (_path: string, options?: { method?: string }) =>
      options?.method === "PUT"
        ? undefined
        : {
            profile: {
              party: { name: "Buyer", tax_id: "********1234" },
              tax: {
                configured: false,
                rate_basis_points: 0,
                included_in_total: true,
              },
              locale: "zh-CN",
            },
          },
  );
  render(<DocumentProfile />);
  const name = await screen.findByDisplayValue("Buyer");
  fireEvent.change(name, { target: { value: "New buyer" } });
  fireEvent.click(
    screen.getByRole("button", { name: "document_profile.save" }),
  );
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/billing/document-profile",
      expect.objectContaining({
        method: "PUT",
        body: { party: { name: "New buyer" }, locale: "zh-CN" },
      }),
    ),
  );
  await screen.findByText("document_profile.saved");
  fireEvent.change(screen.getByDisplayValue("********1234"), {
    target: { value: "" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "document_profile.save" }),
  );
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      "/api/v1/billing/document-profile",
      expect.objectContaining({
        method: "PUT",
        body: { party: { tax_id: "" }, locale: "zh-CN" },
      }),
    ),
  );
});
