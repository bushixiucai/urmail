import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { RemoteImagesSection } from "./remote-images-section";
import * as mailboxApi from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (_key: string, defaultVal?: string) => defaultVal || _key,
  }),
}));

vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => {
  const actual = await importOriginal<typeof mailboxApi>();
  return {
    ...actual,
    getMailboxSettings: vi.fn(),
    saveMailboxSettings: vi.fn(),
  };
});

describe("RemoteImagesSection", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  const baseSettings: mailboxApi.MailboxSettingsView = {
    display_name: "Alice Smith",
    signature_text: "Best regards, Alice",
    signature_html: "<p>Best regards, Alice</p>",
    identity_profiles: {
      "alice@example.com": {
        display_name: "Alice",
        signature_text: "Alice",
        signature_html: "<p>Alice</p>",
      },
    },
    preferences: {
      density: "compact",
      unknown_custom_key: "should_be_preserved",
      ai: { model: "claude-3" },
      allow_all_remote_images: false,
    },
    version: 4,
    updated_at: "2026-11-06T12:00:00Z",
    address: "alice@example.com",
  };

  beforeEach(() => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue(baseSettings);
    vi.mocked(mailboxApi.saveMailboxSettings).mockResolvedValue({
      saved: true,
      version: 5,
    });
  });

  it("interprets absent preference or non-boolean as disabled (fail-closed legacy)", async () => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValueOnce({
      ...baseSettings,
      preferences: {
        density: "compact",
      },
    });

    render(<RemoteImagesSection />);

    await waitFor(() => {
      const toggle = screen.getByTestId("switch-allow-remote-images");
      expect(toggle.getAttribute("aria-checked")).toBe("false");
    });
  });

  it("reads back confirmed true state when allow_all_remote_images is true", async () => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValueOnce({
      ...baseSettings,
      preferences: {
        ...baseSettings.preferences,
        allow_all_remote_images: true,
      },
    });

    render(<RemoteImagesSection />);

    await waitFor(() => {
      const toggle = screen.getByTestId("switch-allow-remote-images");
      expect(toggle.getAttribute("aria-checked")).toBe("true");
    });
  });

  it("submits complete snapshot with preserved preferences and expected_version", async () => {
    const onPrefChanged = vi.fn();
    render(<RemoteImagesSection onPreferenceChanged={onPrefChanged} />);

    await waitFor(() => {
      expect(screen.getByTestId("switch-allow-remote-images")).toBeTruthy();
    });

    const toggle = screen.getByTestId("switch-allow-remote-images");
    expect(toggle.getAttribute("aria-checked")).toBe("false");

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(mailboxApi.saveMailboxSettings).toHaveBeenCalledWith({
        display_name: "Alice Smith",
        signature_text: "Best regards, Alice",
        signature_html: "<p>Best regards, Alice</p>",
        identity_profiles: baseSettings.identity_profiles,
        preferences: {
          density: "compact",
          unknown_custom_key: "should_be_preserved",
          ai: { model: "claude-3" },
          allow_all_remote_images: true,
        },
        expected_version: 4,
      });
      expect(toggle.getAttribute("aria-checked")).toBe("true");
      expect(onPrefChanged).toHaveBeenCalledWith(true);
      expect(screen.getByTestId("privacy-status-success")).toBeTruthy();
    });
  });

  it("handles 409 version conflict by refreshing latest state without blind replay", async () => {
    vi.mocked(mailboxApi.saveMailboxSettings).mockRejectedValueOnce(
      new Error("409: MAILBOX_SETTINGS_VERSION_CONFLICT"),
    );

    const freshSettings: mailboxApi.MailboxSettingsView = {
      ...baseSettings,
      version: 5,
      preferences: {
        ...baseSettings.preferences,
        allow_all_remote_images: false,
        other_new_key: "written_by_another_device",
      },
    };
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValueOnce(
      freshSettings,
    );

    render(<RemoteImagesSection />);

    await waitFor(() => {
      expect(screen.getByTestId("switch-allow-remote-images")).toBeTruthy();
    });

    const toggle = screen.getByTestId("switch-allow-remote-images");
    fireEvent.click(toggle);

    await waitFor(() => {
      expect(screen.getByTestId("privacy-status-conflict")).toBeTruthy();
      expect(screen.queryByTestId("privacy-status-success")).toBeNull();
      // Verified fresh settings were fetched
      expect(mailboxApi.getMailboxSettings).toHaveBeenCalledTimes(2);
      expect(toggle.getAttribute("aria-checked")).toBe("false");
    });
  });

  it("handles network error/timeout by reconciling with GET without claiming success", async () => {
    vi.mocked(mailboxApi.saveMailboxSettings).mockRejectedValueOnce(
      new Error("Network timeout"),
    );

    render(<RemoteImagesSection />);

    await waitFor(() => {
      expect(screen.getByTestId("switch-allow-remote-images")).toBeTruthy();
    });

    const toggle = screen.getByTestId("switch-allow-remote-images");
    fireEvent.click(toggle);

    await waitFor(() => {
      expect(screen.getByTestId("privacy-status-error")).toBeTruthy();
      expect(screen.queryByTestId("privacy-status-success")).toBeNull();
      expect(mailboxApi.getMailboxSettings).toHaveBeenCalledTimes(2);
      expect(toggle.getAttribute("aria-checked")).toBe("false");
    });
  });
});
