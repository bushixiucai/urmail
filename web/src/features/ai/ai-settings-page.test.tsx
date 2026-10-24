import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AISettingsPage } from "./ai-settings-page";
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
describe("AI configuration", () => {
  it("retains redacted credentials until explicitly replaced and clears the input after saving", async () => {
    request.mockImplementation(
      async (path: string, options?: { method?: string }) => {
        if (options?.method === "PUT") return undefined;
        if (path.endsWith("/usage")) return { requests: [] };
        return {
          configuration: {
            mode: "byo",
            provider: "openai",
            endpoint: "https://models.example.test/v1",
            model: "model",
            enabled: true,
            local_only: false,
            credential_configured: true,
          },
          cloud_endpoints: ["https://models.example.test/v1"],
          local_endpoints: [],
        };
      },
    );
    render(<AISettingsPage />);
    const key = (await screen.findByLabelText(
      "page.ai.api_key",
    )) as HTMLInputElement;
    expect(key.value).toBe("");
    fireEvent.change(key, { target: { value: "new-explicit-key" } });
    fireEvent.click(screen.getByRole("button", { name: "action.save" }));
    await waitFor(() =>
      expect(request).toHaveBeenCalledWith(
        "/api/v1/ai/configuration",
        expect.objectContaining({
          method: "PUT",
          body: expect.objectContaining({
            api_key: "new-explicit-key",
            local_only: false,
          }),
        }),
      ),
    );
    await waitFor(() => expect(key.value).toBe(""));
    expect(screen.queryByText("new-explicit-key")).toBeNull();
  });
});
