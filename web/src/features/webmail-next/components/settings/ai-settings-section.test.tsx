import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AiSettingsSection } from "./ai-settings-section";
import * as mailboxApi from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (_key: string, defaultVal?: unknown) =>
      typeof defaultVal === "string" ? defaultVal : _key,
  }),
}));

vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => {
  const actual = await importOriginal<typeof mailboxApi>();
  return {
    ...actual,
    getMailboxSettings: vi.fn(),
    saveMailboxSettings: vi.fn(),
    fetchMailboxAIModels: vi.fn(),
    fetchMailboxAIBudget: vi.fn(),
  };
});

describe("AiSettingsSection", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  const baseSettings: mailboxApi.MailboxSettingsView = {
    display_name: "Tiger Wang",
    signature_text: "Best regards",
    signature_html: "<p>Best regards</p>",
    preferences: {
      density: "comfortable",
      ai: {
        use_custom: false,
        provider: "openai",
        endpoint: "",
        model: "",
        key_configured: false,
      },
    },
    version: 3,
    updated_at: "2026-11-08T00:00:00Z",
    address: "tiger@urmail.cn",
  };

  const mockBudget: mailboxApi.AIBudgetStatus = {
    enabled: true,
    cost_currency: "USD",
    daily: {
      tokens_used: 1200,
      tokens_limit: 100000,
      cost_micros_used: 50000,
      cost_micros_limit: 5000000,
    },
    monthly: {
      tokens_used: 35000,
      tokens_limit: 2000000,
      cost_micros_used: 1200000,
      cost_micros_limit: 50000000,
    },
    entitlement: {
      source: "plan",
      available: true,
      call_limit: 500,
      mailbox_call_limit: 100,
      concurrency_limit: 3,
      period_start: "2026-11-01T00:00:00Z",
      period_end: "2026-11-30T23:59:59Z",
      timezone: "UTC",
      calls_used: 42,
      mailbox_calls_used: 15,
      calls_remaining: 458,
      mailbox_calls_remaining: 85,
      concurrent_reserved: 0,
    },
  };

  beforeEach(() => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue(baseSettings);
    vi.mocked(mailboxApi.fetchMailboxAIBudget).mockResolvedValue(mockBudget);
    vi.mocked(mailboxApi.saveMailboxSettings).mockResolvedValue({
      saved: true,
      version: 4,
    });
    vi.mocked(mailboxApi.fetchMailboxAIModels).mockResolvedValue({
      status: "ok",
      models: ["gpt-4o", "gpt-4o-mini", "deepseek-chat"],
    });
  });

  it("renders initial state with billing banner and unexpanded BYOK", async () => {
    render(<AiSettingsSection />);

    await waitFor(() => {
      expect(screen.getByTestId("ai-settings-section")).toBeTruthy();
    });

    const checkbox = screen.getByTestId("checkbox-use-custom-ai");
    expect((checkbox as HTMLInputElement).checked).toBe(false);

    // BYOK fields should be hidden initially
    expect(screen.queryByTestId("select-ai-provider")).toBeNull();
    expect(screen.queryByTestId("input-ai-endpoint")).toBeNull();

    // Budget card should be rendered
    expect(screen.getByTestId("ai-budget-container")).toBeTruthy();
    expect(screen.getByText("15 / 100")).toBeTruthy();
  });

  it("expands BYOK configuration and handles write-only API key state", async () => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValueOnce({
      ...baseSettings,
      preferences: {
        ...baseSettings.preferences,
        ai: {
          use_custom: true,
          provider: "openai",
          endpoint: "https://api.openai.com/v1",
          model: "gpt-4o",
          key_configured: true,
        },
      },
    });

    render(<AiSettingsSection />);

    await waitFor(() => {
      expect(screen.getByTestId("checkbox-use-custom-ai")).toBeTruthy();
    });

    // Checkbox is checked
    expect(
      (screen.getByTestId("checkbox-use-custom-ai") as HTMLInputElement)
        .checked,
    ).toBe(true);

    // Form inputs are visible
    expect(screen.getByTestId("select-ai-provider")).toBeTruthy();
    expect(screen.getByTestId("input-ai-endpoint")).toBeTruthy();

    // Key is configured: badge rendered, NO password input
    expect(screen.getByTestId("badge-key-configured")).toBeTruthy();
    expect(screen.getByTestId("btn-clear-ai-key")).toBeTruthy();
    expect(screen.queryByTestId("input-ai-api-key")).toBeNull();

    // Click "Clear Key"
    fireEvent.click(screen.getByTestId("btn-clear-ai-key"));

    // Badge disappears and password input is revealed
    expect(screen.queryByTestId("badge-key-configured")).toBeNull();
    const apiKeyInput = screen.getByTestId("input-ai-api-key");
    expect(apiKeyInput).toBeTruthy();

    // Click Save
    fireEvent.click(screen.getByTestId("btn-save-ai-settings"));

    await waitFor(() => {
      expect(mailboxApi.saveMailboxSettings).toHaveBeenCalledWith(
        expect.objectContaining({
          expected_version: 3,
          preferences: expect.objectContaining({
            ai: expect.objectContaining({
              use_custom: true,
              clear_api_key: true,
            }),
          }),
        }),
      );
    });
  });

  it("validates endpoint URL and blocks save when invalid", async () => {
    render(<AiSettingsSection />);

    await waitFor(() => {
      expect(screen.getByTestId("checkbox-use-custom-ai")).toBeTruthy();
    });

    // Turn on BYOK
    fireEvent.click(screen.getByTestId("checkbox-use-custom-ai"));

    const endpointInput = screen.getByTestId("input-ai-endpoint");
    fireEvent.change(endpointInput, {
      target: { value: "invalid-url-no-scheme" },
    });

    // Error message shown
    expect(
      screen.getByText("Endpoint URL must start with http:// or https://"),
    ).toBeTruthy();

    // Save button disabled
    const saveBtn = screen.getByTestId(
      "btn-save-ai-settings",
    ) as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(true);

    // Fix URL
    fireEvent.change(endpointInput, {
      target: { value: "https://api.deepseek.com/v1" },
    });
    expect(saveBtn.disabled).toBe(false);
  });

  it("tests connection and loads models", async () => {
    render(<AiSettingsSection />);

    await waitFor(() => {
      expect(screen.getByTestId("checkbox-use-custom-ai")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("checkbox-use-custom-ai"));

    const endpointInput = screen.getByTestId("input-ai-endpoint");
    fireEvent.change(endpointInput, {
      target: { value: "https://api.openai.com/v1" },
    });

    const apiKeyInput = screen.getByTestId("input-ai-api-key");
    fireEvent.change(apiKeyInput, { target: { value: "sk-test-secret" } });

    const testBtn = screen.getByTestId("btn-test-ai-models");
    fireEvent.click(testBtn);

    await waitFor(() => {
      expect(mailboxApi.fetchMailboxAIModels).toHaveBeenCalledWith({
        provider: "openai",
        endpoint: "https://api.openai.com/v1",
        model: undefined,
        api_key: "sk-test-secret",
      });
      expect(screen.getByTestId("test-connection-result")).toBeTruthy();
    });

    // Auto-populates first model if empty
    const modelInput = screen.getByTestId("input-ai-model") as HTMLInputElement;
    expect(modelInput.value).toBe("gpt-4o");
  });

  it("handles test connection failure gracefully", async () => {
    vi.mocked(mailboxApi.fetchMailboxAIModels).mockRejectedValueOnce(
      new Error("ECONNREFUSED"),
    );

    render(<AiSettingsSection />);

    await waitFor(() => {
      expect(screen.getByTestId("checkbox-use-custom-ai")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("checkbox-use-custom-ai"));

    const testBtn = screen.getByTestId("btn-test-ai-models");
    fireEvent.click(testBtn);

    await waitFor(() => {
      expect(
        screen.getByText(
          "Connection test failed, please check endpoint and key",
        ),
      ).toBeTruthy();
    });
  });

  it("handles 409 conflict and refreshes settings", async () => {
    const conflictError = Object.assign(new Error("Conflict"), { status: 409 });
    vi.mocked(mailboxApi.saveMailboxSettings).mockRejectedValueOnce(
      conflictError,
    );

    render(<AiSettingsSection />);

    await waitFor(() => {
      expect(screen.getByTestId("btn-save-ai-settings")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("btn-save-ai-settings"));

    await waitFor(() => {
      expect(
        screen.getByText(
          "Settings were modified by another client (version conflict). Please refresh and retry.",
        ),
      ).toBeTruthy();
      // Refreshed getMailboxSettings
      expect(mailboxApi.getMailboxSettings).toHaveBeenCalledTimes(2);
    });
  });
});
