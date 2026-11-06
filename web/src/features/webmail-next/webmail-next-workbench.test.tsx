import {
  render,
  screen,
  fireEvent,
  cleanup,
  waitFor,
  act,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { WebmailNextWorkbench } from "./webmail-next-workbench";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import * as mailboxApi from "@/features/webmail/mailbox-api";
import { REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT } from "./components/settings/remote-images-section";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => {
  const actual = await importOriginal<typeof mailboxApi>();
  return {
    ...actual,
    listFolders: vi.fn(),
    listMessages: vi.fn(),
    getMessage: vi.fn(),
    getMailboxSettings: vi.fn(),
    getMailboxRules: vi.fn(),
  };
});

const createTestQueryClient = () =>
  new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

describe("WebmailNextWorkbench Layout", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = createTestQueryClient();
    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [{ name: "Inbox", name_enc: "INBOX", delimiter: "/" }],
    });
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [
        {
          uid: 101,
          subject: "Message 1",
          from: [{ name: "Sender 1", email: "sender1@example.com" }],
          date: "2026-11-06T10:00:00Z",
          flags: ["\\Seen"],
          size: 1024,
        },
        {
          uid: 102,
          subject: "Message 2",
          from: [{ name: "Sender 2", email: "sender2@example.com" }],
          date: "2026-11-06T11:00:00Z",
          flags: ["\\Seen"],
          size: 2048,
        },
      ],
      total: 2,
      limit: 100,
      offset: 0,
    });
    vi.mocked(mailboxApi.getMessage).mockImplementation((_folder, uid, opts) =>
      Promise.resolve({
        uid,
        subject: `Message ${uid}`,
        from: [{ name: "Sender", email: "sender@example.com" }],
        to: [{ name: "Recipient", email: "me@example.com" }],
        date: "2026-11-06T10:00:00Z",
        flags: ["\\Seen"],
        size: 1024,
        html: opts?.loadImages
          ? "<p>Hello <img src='https://tracker.com/pixel.png'/></p>"
          : "<p>Hello</p>",
        notes: opts?.loadImages ? [] : ["remote_images_blocked"],
        attachments: [],
      }),
    );
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
      display_name: "User",
      signature_text: "",
      signature_html: "",
      preferences: { allow_all_remote_images: false },
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "me@example.com",
    });
    vi.mocked(mailboxApi.getMailboxRules).mockResolvedValue({
      version: "v1",
      state: "active",
      rules: {
        rules: [],
        vacation: { enabled: false, subject: "", message: "", days: 1 },
      },
    });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("renders header, navigation rail, list, and reading pane", () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(screen.getByTestId("webmail-header")).toBeTruthy();
    expect(screen.getByTestId("webmail-navigation-rail")).toBeTruthy();
    expect(screen.getByTestId("webmail-message-list")).toBeTruthy();
    expect(screen.getByTestId("webmail-reading-pane")).toBeTruthy();
  });

  it("toggles rail collapse to mini mode", () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const rail = screen.getByTestId("webmail-navigation-rail");
    expect(rail.getAttribute("data-collapsed")).toBe("false");

    const toggleBtn = screen.getByTestId("rail-toggle-button");
    fireEvent.click(toggleBtn);
    expect(rail.getAttribute("data-collapsed")).toBe("true");
  });

  it("toggles density mode through 3 tiers (compact -> comfortable -> list_only -> compact)", () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const densityBtn = screen.getByTestId("webmail-density-toggle");
    expect(densityBtn.textContent).toMatch(/compact/i);
    fireEvent.click(densityBtn);
    expect(densityBtn.textContent).toMatch(/comfortable/i);
    fireEvent.click(densityBtn);
    expect(densityBtn.textContent).toMatch(/list_only/i);
    fireEvent.click(densityBtn);
    expect(densityBtn.textContent).toMatch(/compact/i);
  });

  it("requests message with loadImages: false by default when global setting is off", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText("Message 1")).toBeTruthy();
    });

    // Click on message 101 in list
    fireEvent.click(screen.getByText("Message 1"));

    await waitFor(() => {
      expect(mailboxApi.getMessage).toHaveBeenCalledWith("INBOX", 101, {
        loadImages: false,
      });
      // Banner must be visible because loadImages is false
      expect(screen.getByTestId("remote-images-banner")).toBeTruthy();
    });
  });

  it("requests message with loadImages: true when global allow_all_remote_images is true", async () => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValueOnce({
      display_name: "User",
      signature_text: "",
      signature_html: "",
      preferences: { allow_all_remote_images: true },
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "me@example.com",
    });

    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText("Message 1")).toBeTruthy();
    });

    fireEvent.click(screen.getByText("Message 1"));

    await waitFor(() => {
      expect(mailboxApi.getMessage).toHaveBeenCalledWith("INBOX", 101, {
        loadImages: true,
      });
      // Banner must be hidden because global images are allowed
      expect(screen.queryByTestId("remote-images-banner")).toBeNull();
    });
  });

  it("switches to loadImages: true on manual consent and preserves consent when global setting is disabled", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText("Message 1")).toBeTruthy();
    });

    fireEvent.click(screen.getByText("Message 1"));

    await waitFor(() => {
      expect(screen.getByTestId("remote-images-banner")).toBeTruthy();
    });

    // Click show images button (manual grant)
    const showBtn = screen.getByTestId("show-images-button");
    fireEvent.click(showBtn);

    await waitFor(() => {
      expect(mailboxApi.getMessage).toHaveBeenCalledWith("INBOX", 101, {
        loadImages: true,
      });
    });

    // Simulate global preference change event toggling to false
    window.dispatchEvent(
      new CustomEvent(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, {
        detail: { allowAll: false },
      }),
    );

    // Manual consent for message 101 should NOT be revoked by global toggle
    await waitFor(() => {
      expect(screen.queryByTestId("remote-images-banner")).toBeNull();
    });
  });

  it("never prefetches remote images for unselected messages", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(screen.getByText("Message 1")).toBeTruthy();
      expect(screen.getByText("Message 2")).toBeTruthy();
    });

    // Click on message 101
    fireEvent.click(screen.getByText("Message 1"));

    await waitFor(() => {
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        101,
        expect.anything(),
      );
    });

    // Verify Message 102 was NEVER fetched with getMessage (no prefetch)
    const calls = vi.mocked(mailboxApi.getMessage).mock.calls;
    const fetchedUids = calls.map((c) => c[1]);
    expect(fetchedUids).not.toContain(102);
  });

  it("drops an already loaded remote body as soon as global allow is turned off, without waiting for settings refetch", async () => {
    const allow = (value: boolean): mailboxApi.MailboxSettingsView => ({
      display_name: "User",
      signature_text: "",
      signature_html: "",
      preferences: { allow_all_remote_images: value },
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "me@example.com",
    });
    let settingsCalls = 0;
    vi.mocked(mailboxApi.getMailboxSettings).mockImplementation(
      () =>
        new Promise((resolve) => {
          settingsCalls += 1;
          if (settingsCalls === 1) {
            resolve(allow(true));
            return;
          }
          // Verification GET stays pending. Authorization must not wait for it.
        }),
    );

    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      const frame = screen.getByTestId("mail-html-frame");
      expect(frame.getAttribute("srcDoc")).toContain(
        "https://tracker.com/pixel.png",
      );
    });

    const callsBefore = vi.mocked(mailboxApi.getMessage).mock.calls.length;
    window.dispatchEvent(
      new CustomEvent(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, {
        detail: { allowAll: false },
      }),
    );

    await waitFor(() => {
      const frame = screen.getByTestId("mail-html-frame");
      const srcDoc = frame.getAttribute("srcDoc") ?? "";
      expect(srcDoc).not.toContain("https://tracker.com/pixel.png");
      expect(srcDoc).toContain("Hello");
      const later = vi
        .mocked(mailboxApi.getMessage)
        .mock.calls.slice(callsBefore);
      expect(later.some((call) => call[2]?.loadImages === true)).toBe(false);
      expect(
        later.some((call) => call[1] === 101 && call[2]?.loadImages === false),
      ).toBe(true);
    });
  });

  it("does not reopen global images when the confirmation GET still returns true", async () => {
    const allow = (value: boolean): mailboxApi.MailboxSettingsView => ({
      display_name: "User",
      signature_text: "",
      signature_html: "",
      preferences: { allow_all_remote_images: value },
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "me@example.com",
    });
    let releaseConfirmation:
      ((value: mailboxApi.MailboxSettingsView) => void) | undefined;
    let settingsCalls = 0;
    vi.mocked(mailboxApi.getMailboxSettings).mockImplementation(
      () =>
        new Promise((resolve) => {
          settingsCalls += 1;
          if (settingsCalls === 1) {
            resolve(allow(true));
            return;
          }
          releaseConfirmation = resolve;
        }),
    );

    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(
        screen.getByTestId("mail-html-frame").getAttribute("srcDoc"),
      ).toContain("https://tracker.com/pixel.png");
    });

    const callsBefore = vi.mocked(mailboxApi.getMessage).mock.calls.length;
    window.dispatchEvent(
      new CustomEvent(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, {
        detail: { allowAll: false },
      }),
    );

    await waitFor(() => {
      expect(releaseConfirmation).toBeTypeOf("function");
      expect(
        screen.getByTestId("mail-html-frame").getAttribute("srcDoc") ?? "",
      ).not.toContain("https://tracker.com/pixel.png");
    });

    await act(async () => {
      releaseConfirmation?.(allow(true));
    });

    await waitFor(() => {
      const srcDoc =
        screen.getByTestId("mail-html-frame").getAttribute("srcDoc") ?? "";
      expect(srcDoc).not.toContain("https://tracker.com/pixel.png");
      const later = vi
        .mocked(mailboxApi.getMessage)
        .mock.calls.slice(callsBefore);
      expect(later.some((call) => call[2]?.loadImages === true)).toBe(false);
    });
  });

  it("does not let an older settings GET undo a newer confirmed allow value", async () => {
    const allow = (value: boolean): mailboxApi.MailboxSettingsView => ({
      display_name: "User",
      signature_text: "",
      signature_html: "",
      preferences: { allow_all_remote_images: value },
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "me@example.com",
    });
    const pending: Array<(value: mailboxApi.MailboxSettingsView) => void> = [];
    let settingsCalls = 0;
    vi.mocked(mailboxApi.getMailboxSettings).mockImplementation(
      () =>
        new Promise((resolve) => {
          settingsCalls += 1;
          if (settingsCalls === 1) {
            resolve(allow(true));
            return;
          }
          pending.push(resolve);
        }),
    );

    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(
        screen.getByTestId("mail-html-frame").getAttribute("srcDoc"),
      ).toContain("https://tracker.com/pixel.png");
    });

    window.dispatchEvent(
      new CustomEvent(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, {
        detail: { allowAll: false },
      }),
    );
    window.dispatchEvent(
      new CustomEvent(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, {
        detail: { allowAll: true },
      }),
    );

    await waitFor(() => {
      expect(pending.length).toBeGreaterThanOrEqual(2);
      expect(
        screen.getByTestId("mail-html-frame").getAttribute("srcDoc"),
      ).toContain("https://tracker.com/pixel.png");
    });

    const callsAtGrant = vi.mocked(mailboxApi.getMessage).mock.calls.length;
    await act(async () => {
      pending[0]?.(allow(false));
    });

    await waitFor(() => {
      const srcDoc =
        screen.getByTestId("mail-html-frame").getAttribute("srcDoc") ?? "";
      expect(srcDoc).toContain("https://tracker.com/pixel.png");
      const later = vi
        .mocked(mailboxApi.getMessage)
        .mock.calls.slice(callsAtGrant);
      expect(later.some((call) => call[2]?.loadImages === false)).toBe(false);
    });
  });
});
