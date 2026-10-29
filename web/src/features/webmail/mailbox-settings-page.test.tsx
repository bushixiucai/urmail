vi.mock("@/components/appearance-controls", () => ({
  AppearanceControls: () => null,
}));
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { MailboxSettingsPage } from "./mailbox-settings-page";
import type { MailboxRulesView, MailboxSettingsView } from "./mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, params?: Record<string, unknown>) =>
      params && "used" in params ? `${k}:${params.used}/${params.total}` : k,
  }),
}));

const getMailboxSettings = vi.fn<() => Promise<MailboxSettingsView>>();
const listMailboxIdentities = vi.fn();
const getMailboxQuota = vi.fn();
const getMailboxRules = vi.fn<() => Promise<MailboxRulesView>>();
const saveMailboxSettings = vi.fn();
const saveMailboxRules = vi.fn();

vi.mock("./mailbox-api", () => ({
  ApiRequestError: class extends Error {},
  getMailboxSettings: (...a: unknown[]) => getMailboxSettings(...(a as [])),
  listMailboxIdentities: (...a: unknown[]) =>
    listMailboxIdentities(...(a as [])),
  getMailboxQuota: (...a: unknown[]) => getMailboxQuota(...(a as [])),
  getMailboxRules: (...a: unknown[]) => getMailboxRules(...(a as [])),
  saveMailboxSettings: (...a: unknown[]) =>
    saveMailboxSettings(...(a as [never])),
  saveMailboxRules: (...a: unknown[]) => saveMailboxRules(...a),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("renders settings sections with identity, quota and rules", async () => {
  getMailboxSettings.mockResolvedValue({
    display_name: "Alice",
    signature_text: "",
    signature_html: "",
    preferences: {},
    version: 3,
    updated_at: "2026-01-01T00:00:00Z",
    address: "alice@example.test",
  });
  listMailboxIdentities.mockResolvedValue({
    identities: [
      { address: "alice@example.test", primary: true },
      { address: "team@example.test", primary: false },
    ],
  });
  getMailboxQuota.mockResolvedValue({ used_bytes: 2048, quota_bytes: 8192 });
  getMailboxRules.mockResolvedValue({
    rules: {
      rules: [],
      vacation: { enabled: false, subject: "", message: "", days: 1 },
    },
    version: "0".repeat(64),
    state: "active",
  });

  const view = render(
    <MemoryRouter>
      <MailboxSettingsPage />
    </MemoryRouter>,
  );
  expect(view.getByText("page.webmail.settings_title")).toBeTruthy();
  expect(view.getByText("page.webmail.settings_identity")).toBeTruthy();
  await waitFor(() => {
    expect(view.getAllByText("team@example.test").length).toBeGreaterThan(0);
  });
  expect(view.getByText("page.webmail.settings_primary")).toBeTruthy();
  await waitFor(() => {
    expect(
      view.getByText((_, element) =>
        Boolean(
          element?.textContent?.startsWith(
            "page.webmail.settings_quota_usage:",
          ),
        ),
      ),
    ).toBeTruthy();
  });
  expect(
    (
      view.getByLabelText(
        "page.webmail.settings_display_name",
      ) as HTMLInputElement
    ).value,
  ).toBe("Alice");
});

it("inserts a signature and saves with the optimistic version", async () => {
  getMailboxSettings.mockResolvedValue({
    display_name: "",
    signature_text: "",
    signature_html: "",
    preferences: {},
    version: 3,
    updated_at: "2026-01-01T00:00:00Z",
    address: "alice@example.test",
  });
  listMailboxIdentities.mockResolvedValue({ identities: [] });
  getMailboxQuota.mockResolvedValue({ used_bytes: 0, quota_bytes: 1024 });
  getMailboxRules.mockResolvedValue({
    rules: {
      rules: [],
      vacation: { enabled: false, subject: "", message: "", days: 1 },
    },
    version: "0".repeat(64),
    state: "active",
  });
  saveMailboxSettings.mockResolvedValue({ saved: true, version: 4 });

  const view = render(
    <MemoryRouter>
      <MailboxSettingsPage />
    </MemoryRouter>,
  );
  const signature = (await waitFor(() =>
    view.getByLabelText("page.webmail.settings_signature_text"),
  )) as HTMLTextAreaElement;
  fireEvent.change(signature, { target: { value: "Best,\nAlice" } });
  fireEvent.click(view.getAllByText("page.webmail.settings_save")[0]);

  await waitFor(() => {
    expect(saveMailboxSettings).toHaveBeenCalled();
  });
  expect(saveMailboxSettings).toHaveBeenCalledWith(
    expect.objectContaining({
      signature_text: "Best,\nAlice",
      expected_version: 3,
    }),
  );
  expect(await view.findByText("page.webmail.settings_saved")).toBeTruthy();
});

it("keeps an untouched later DST occurrence when saving other rule edits", async () => {
  getMailboxSettings.mockResolvedValue({
    address: "alice@example.test",
    display_name: "Alice",
    signature_text: "",
    signature_html: "",
    preferences: { timezone: "America/New_York" },
    version: 1,
    updated_at: "",
  });
  listMailboxIdentities.mockResolvedValue({
    identities: [{ address: "alice@example.test", primary: true }],
  });
  getMailboxQuota.mockResolvedValue({ used_bytes: 0, quota_bytes: 1024 });
  getMailboxRules.mockResolvedValue({
    state: "active",
    version: "a".repeat(64),
    rules: {
      rules: [],
      vacation: {
        enabled: true,
        subject: "Away",
        message: "Actual body",
        days: 1,
        from: "2026-11-01T06:30:00Z",
        until: "2026-11-01T07:30:00Z",
      },
    },
  });
  saveMailboxRules.mockResolvedValue({
    saved: true,
    state: "active",
    version: "b".repeat(64),
  });
  const view = render(
    <MemoryRouter>
      <MailboxSettingsPage />
    </MemoryRouter>,
  );
  await waitFor(() =>
    expect(
      (view.container.querySelector("#vacation-from") as HTMLInputElement)
        ?.value,
    ).toBe("2026-11-01T01:30"),
  );
  fireEvent.click(view.getByLabelText("page.webmail.settings_rules_confirm"));
  fireEvent.click(view.getAllByText("page.webmail.settings_save")[1]);
  await waitFor(() =>
    expect(saveMailboxRules).toHaveBeenCalledWith(
      expect.objectContaining({
        confirmed: true,
        rules: expect.objectContaining({
          vacation: expect.objectContaining({
            from: "2026-11-01T06:30:00Z",
            until: "2026-11-01T07:30:00Z",
            message: "Actual body",
          }),
        }),
      }),
    ),
  );
});
