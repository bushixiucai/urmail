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
    getMailboxQuota: vi.fn(),
    moveMessages: vi.fn(),
    deleteMessages: vi.fn(),
    createMailboxFolder: vi.fn(),
    flagMessages: vi.fn(),
    listScheduledSends: vi.fn(),
    crossFolderSearch: vi.fn(),
  };
});

vi.mock("./lib/thread-members-api", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("./lib/thread-members-api")>();
  return { ...actual, fetchThreadMembers: vi.fn() };
});

import { fetchThreadMembers } from "./lib/thread-members-api";

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
      uid_validity: 7,
    });
    vi.mocked(mailboxApi.getMailboxQuota).mockRejectedValue(new Error("quota"));
    vi.mocked(mailboxApi.moveMessages).mockResolvedValue({
      moved: 1,
      to: "Trash",
    });
    vi.mocked(mailboxApi.deleteMessages).mockResolvedValue({
      deleted: 1,
      expunged: false,
    });
    vi.mocked(mailboxApi.createMailboxFolder).mockImplementation(
      async (name) => ({
        folder: { name, name_enc: name, delimiter: "/" },
      }),
    );
    vi.mocked(mailboxApi.flagMessages).mockResolvedValue({ updated: 1 });
    vi.mocked(mailboxApi.listScheduledSends).mockResolvedValue({ items: [] });
    vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
      messages: [],
      total: 0,
      q: "",
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
    // Undo refetches thread members; a thrown/undefined page would surface as
    // an unhandled rejection in infinite-query (and fail CI), so always answer.
    vi.mocked(fetchThreadMembers).mockResolvedValue({
      thread_id: 101,
      total: 0,
      unread_count: 0,
      scope: "server",
      messages: [],
      has_more_older: false,
    } as never);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("renders header, navigation rail, list, and reading pane", () => {
    const { container } = render(
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
    // The root must not disable text selection: message bodies are copyable.
    const root = container.querySelector(".webmail-vellum-root");
    expect(root?.className).not.toContain("select-none");
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

  it("sets density from a checked menu instead of cycling an enum", () => {
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
    const comfortable = screen.getByTestId("webmail-density-comfortable");
    expect(comfortable.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(comfortable);
    expect(document.documentElement.className).toContain("density-comfortable");
    fireEvent.click(densityBtn);
    expect(
      screen
        .getByTestId("webmail-density-comfortable")
        .getAttribute("aria-checked"),
    ).toBe("true");
    fireEvent.click(screen.getByTestId("webmail-density-list_only"));
    expect(document.documentElement.className).toContain("density-list_only");
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

  it("moves the cursor with j and opens only with o", async () => {
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
    fireEvent.keyDown(window, { key: "j" });
    expect(screen.getByTestId("mail-row-102").getAttribute("data-cursor")).toBe(
      "true",
    );
    const opened = vi
      .mocked(mailboxApi.getMessage)
      .mock.calls.map((call) => call[1]);
    expect(opened).not.toContain(102);
    fireEvent.keyDown(window, { key: "o" });
    await waitFor(() => {
      expect(
        vi.mocked(mailboxApi.getMessage).mock.calls.map((call) => call[1]),
      ).toContain(102);
    });
  });

  it("archives with e and moves to Trash with # without deleting", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    fireEvent.keyDown(window, { key: "e" });
    await waitFor(() => {
      expect(mailboxApi.createMailboxFolder).toHaveBeenCalledWith("Archive");
      expect(mailboxApi.moveMessages).toHaveBeenCalledWith(
        "INBOX",
        expect.objectContaining({ to: "Archive", uid_validity: 7 }),
      );
    });
    expect(mailboxApi.deleteMessages).not.toHaveBeenCalled();
    const notice = screen.getByTestId("webmail-move-notice");
    expect(notice.querySelector("button")).toBeNull();

    fireEvent.keyDown(window, { key: "#" });
    await waitFor(() => {
      expect(mailboxApi.createMailboxFolder).toHaveBeenCalledWith("Trash");
      expect(mailboxApi.moveMessages).toHaveBeenCalledWith(
        "INBOX",
        expect.objectContaining({ to: "Trash" }),
      );
    });
    expect(mailboxApi.deleteMessages).not.toHaveBeenCalled();
  });

  it("keeps the open message when a loaded selection is archived", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    fireEvent.click(screen.getByTestId("mail-check-101"));
    fireEvent.click(screen.getByTestId("mail-check-102"), { shiftKey: true });
    expect(screen.getByTestId("bulk-selected-label").textContent).toContain(
      "webmail_next.selected_loaded",
    );
    fireEvent.click(screen.getByTestId("bulk-archive"));
    await waitFor(() => {
      const body = vi.mocked(mailboxApi.moveMessages).mock.calls.at(-1)?.[1];
      expect(body?.uids).toEqual(expect.arrayContaining([101, 102]));
    });
    expect(screen.getByTestId("mail-row-101").getAttribute("data-active")).toBe(
      "true",
    );
  });

  it("hides the quota bar when quota is unavailable and shows a real total", async () => {
    const view = render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    expect(screen.queryByTestId("webmail-quota-bar")).toBeNull();
    expect(screen.queryByText("1.2 GB / 5.0 GB")).toBeNull();
    expect(screen.queryByTestId("nav-folder-Starred")).toBeNull();
    view.unmount();

    vi.mocked(mailboxApi.getMailboxQuota).mockResolvedValue({
      used_bytes: 100,
      quota_bytes: 1000,
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("webmail-quota-bar").textContent).toContain(
        "10%",
      );
    });
    expect(screen.getByTestId("webmail-quota-bar").textContent).not.toContain(
      "1.2 GB",
    );
  });

  it("loads scheduled mail from listScheduledSends", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    fireEvent.click(screen.getByTestId("nav-folder-scheduled"));
    await waitFor(() => {
      expect(mailboxApi.listScheduledSends).toHaveBeenCalled();
    });
    const folders = vi
      .mocked(mailboxApi.listMessages)
      .mock.calls.map((call) => call[0]);
    expect(folders).not.toContain("Scheduled");
    expect(screen.getByTestId("webmail-scheduled-list")).toBeTruthy();
  });

  it("searches on enter, shows the search state immediately, and opens the source folder", async () => {
    vi.mocked(mailboxApi.crossFolderSearch).mockImplementation(
      () =>
        new Promise((resolve) => {
          setTimeout(
            () =>
              resolve({
                messages: [
                  {
                    uid: 501,
                    subject: "Found note",
                    from: [{ name: "Ann", email: "ann@example.test" }],
                    date: "2026-11-06T12:00:00Z",
                    flags: ["\\Seen"],
                    size: 10,
                    folder: "Archive",
                    folder_enc: "ARCHIVE",
                  },
                ],
                total: 1,
                q: "hello",
              }),
            400,
          );
        }),
    );
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    const input = screen.getByTestId("webmail-search-input");
    fireEvent.change(input, { target: { value: "hello" } });
    expect(mailboxApi.crossFolderSearch).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByTestId("webmail-search-status")).toBeTruthy();
    expect(mailboxApi.crossFolderSearch).not.toHaveBeenCalled();
    await waitFor(() => {
      expect(mailboxApi.crossFolderSearch).toHaveBeenCalledWith(
        "hello",
        { limit: 100 },
        expect.anything(),
      );
    });
    await waitFor(() => expect(screen.getByText("Found note")).toBeTruthy());
    expect(screen.getByTestId("mail-folder-501").textContent).toBe("Archive");
    fireEvent.click(screen.getByTestId("mail-row-501"));
    await waitFor(() => {
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "ARCHIVE",
        501,
        expect.anything(),
      );
    });
  });

  it("shows a reading skeleton as soon as a message is opened", async () => {
    const detail: mailboxApi.MessageDetail = {
      uid: 101,
      subject: "Message 101",
      from: [{ name: "Sender", email: "sender@example.com" }],
      to: [{ name: "Recipient", email: "me@example.com" }],
      date: "2026-11-06T10:00:00Z",
      flags: ["\\Seen"],
      size: 1024,
      html: "<p>Hello</p>",
      notes: ["remote_images_blocked"],
      attachments: [],
    };
    let settled: mailboxApi.MessageDetail | null = null;
    const waiters: Array<(value: mailboxApi.MessageDetail) => void> = [];
    vi.mocked(mailboxApi.getMessage).mockImplementation(
      () =>
        new Promise((resolve) => {
          if (settled) resolve(settled);
          else waiters.push(resolve);
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
      expect(screen.getByTestId("reading-skeleton")).toBeTruthy();
    });
    settled = detail;
    await act(async () => {
      waiters.splice(0).forEach((resolve) => resolve(detail));
    });
    await waitFor(() => {
      expect(screen.queryByTestId("reading-skeleton")).toBeNull();
    });
  });

  it("undoes a move only when destination_uids align with the request", async () => {
    vi.mocked(mailboxApi.moveMessages).mockResolvedValue({
      moved: 1,
      to: "Archive",
      destination_uids: [880],
      destination_uid_validity: 11,
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    fireEvent.keyDown(window, { key: "e" });
    await waitFor(() => {
      expect(screen.getByTestId("webmail-move-undo")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("webmail-move-undo"));
    await waitFor(() => {
      expect(mailboxApi.moveMessages).toHaveBeenLastCalledWith("Archive", {
        uids: [880],
        to: "INBOX",
        uid_validity: 11,
      });
    });
  });

  it("omits undo when destination_uids are missing or a different length", async () => {
    vi.mocked(mailboxApi.moveMessages).mockResolvedValue({
      moved: 1,
      to: "Archive",
      destination_uids: [880, 881],
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    fireEvent.keyDown(window, { key: "e" });
    await waitFor(
      () => {
        expect(screen.getByTestId("webmail-move-notice")).toBeTruthy();
      },
      { timeout: 5000 },
    );
    expect(screen.queryByTestId("webmail-move-undo")).toBeNull();
  });

  it("renders decoded folder name in header when initialized with b64 encoded folder", async () => {
    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [
        {
          name: "INBOX",
          name_enc: "b64.SU5CT1g",
          delimiter: "/",
          flags: ["\\Inbox"],
        },
        { name: "工作", name_enc: "b64.5bel5L2c", delimiter: "/", flags: [] },
      ],
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench initialFolder="b64.SU5CT1g" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    expect(screen.getByTestId("folder-name-header").textContent).toBe(
      "webmail_next.inbox",
    );
  });

  it("renders decoded Chinese folder name in header when initialized with custom b64 folder", async () => {
    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [
        {
          name: "INBOX",
          name_enc: "b64.SU5CT1g",
          delimiter: "/",
          flags: ["\\Inbox"],
        },
        { name: "工作", name_enc: "b64.5bel5L2c", delimiter: "/", flags: [] },
      ],
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench initialFolder="b64.5bel5L2c" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText("Message 1")).toBeTruthy());
    expect(screen.getByTestId("folder-name-header").textContent).toBe("工作");
  });
});

describe("same-uid cross-box interactions (#122 release gate)", () => {
  let queryClient: QueryClient;

  const INBOX_148 = {
    uid: 148,
    subject: "cross-box sample",
    from: [{ name: "Ann", email: "ann@x" }],
    date: "2026-11-06T10:00:00Z",
    flags: [],
    size: 1,
    thread_id: 148,
    thread_count: 2,
    folder: "INBOX",
    folder_enc: "INBOX",
    uid_validity: 7,
  };
  const SENT_148 = {
    uid: 148,
    subject: "Re: cross-box sample",
    from: [{ name: "Me", email: "me@x" }],
    date: "2026-11-06T11:00:00Z",
    flags: [],
    size: 1,
    thread_id: 148,
    thread_count: 2,
    folder: "Sent",
    folder_enc: "Sent",
    uid_validity: 9,
  };

  const renderWorkbench = () =>
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );

  beforeEach(() => {
    queryClient = createTestQueryClient();
    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [
        { name: "Inbox", name_enc: "INBOX", delimiter: "/", flags: [] },
        {
          name: "Sent",
          name_enc: "Sent",
          delimiter: "/",
          flags: ["\\Sent"],
        },
        {
          name: "Trash",
          name_enc: "Trash",
          delimiter: "/",
          flags: ["\\Trash"],
        },
      ] as never,
    });
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [INBOX_148],
      total: 1,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    vi.mocked(fetchThreadMembers).mockResolvedValue({
      thread_id: 148,
      total: 2,
      unread_count: 0,
      scope: "server",
      messages: [INBOX_148, SENT_148] as never,
      has_more_older: false,
      oldest: "INBOX:148",
    });
    vi.mocked(mailboxApi.getMessage).mockImplementation(((
      _folder: string,
      uid: number,
    ) =>
      Promise.resolve({
        uid,
        subject: `detail ${uid}`,
        from: [{ name: "S", email: "s@x" }],
        to: [],
        date: "2026-11-06T10:00:00Z",
        flags: [],
        size: 1,
        html: "<p>body</p>",
        notes: [],
        attachments: [],
      })) as never);
    vi.mocked(mailboxApi.getMailboxQuota).mockRejectedValue(new Error("q"));
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
      display_name: "User",
      signature_text: "",
      signature_html: "",
    } as never);
    vi.mocked(mailboxApi.getMailboxRules).mockResolvedValue({
      rules: [],
    } as never);
    vi.mocked(mailboxApi.listScheduledSends).mockResolvedValue({ items: [] });
    vi.mocked(mailboxApi.moveMessages).mockReset();
    vi.mocked(mailboxApi.moveMessages).mockResolvedValue({
      moved: 1,
      to: "Trash",
    });
  });

  it("opens the INBOX copy from a list click (no hint prefers browsing folder)", async () => {
    renderWorkbench();
    const row = await screen.findByTestId("mail-row-148");
    fireEvent.click(row);
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
  });

  it("expanding the Sent card reads the Sent copy", async () => {
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    expect(headers).toHaveLength(2);
    fireEvent.click(headers[1]); // newest = Sent
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "Sent",
        148,
        expect.anything(),
      ),
    );
  });

  it("anchors on the Sent copy even while the INBOX same-uid row is still listed", async () => {
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[1]); // expand Sent
    await waitFor(() => {
      const calls = vi.mocked(fetchThreadMembers).mock.calls;
      const last = calls[calls.length - 1];
      expect(last?.[0]).toBe("Sent");
      expect(last?.[1]).toBe(148);
    });
  });

  it("never fires the members API for an unverified list row", async () => {
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [
        {
          uid: 300,
          subject: "unverified bounce",
          from: [{ name: "M", email: "m@x" }],
          date: "2026-11-06T10:00:00Z",
          flags: [],
          size: 1,
          thread_verification: "unverified",
        },
      ],
      total: 1,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    renderWorkbench();
    const row = await screen.findByTestId("mail-row-300");
    fireEvent.click(row);
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        300,
        expect.anything(),
      ),
    );
    expect(fetchThreadMembers).not.toHaveBeenCalled();
  });

  it("keeps the verified anchor when a refresh removes the auto-selected row", async () => {
    // Wide screen auto-selects the verified INBOX 148 row (thread_id set)
    // → stackOrigin(INBOX,148) is recorded. A later refresh that removes
    // every list row must NOT deactivate the members query (QA sequence ①,
    // persistence variant): the anchor is the verified thread origin.
    renderWorkbench();
    await waitFor(() =>
      expect(fetchThreadMembers).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
    const callsAtSteady = vi.mocked(fetchThreadMembers).mock.calls.length;
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [],
      total: 0,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    fireEvent.click(screen.getByTestId("webmail-refresh-btn"));
    await waitFor(() =>
      expect(
        vi.mocked(mailboxApi.listMessages).mock.calls.length,
      ).toBeGreaterThanOrEqual(2),
    );
    // The anchor must never switch boxes: the last members target stays
    // (INBOX,148) even after the row is gone. Framework may legitimately
    // re-run the same-key query (bounded), so assert on the TARGET only.
    await new Promise((r) => setTimeout(r, 50));
    const calls = vi.mocked(fetchThreadMembers).mock.calls;
    expect(calls.length).toBeLessThan(callsAtSteady + 3); // bounded churn
    const last = calls[calls.length - 1];
    expect(last?.[0]).toBe("INBOX");
    expect(last?.[1]).toBe(148);
  });

  it("safely ends the query instead of substituting a foreign same-uid row", async () => {
    // Unverified INBOX row (no thread_id) opened from the list; refresh
    // removes it while a verified Sent 148 row remains. No stackOrigin and
    // no bare-uid substitution are allowed → the members query must not
    // keep firing against the Sent copy.
    const unverifiedInbox = {
      ...INBOX_148,
      thread_id: undefined,
      thread_count: undefined,
      thread_verification: "unverified",
    };
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [unverifiedInbox, SENT_148],
      total: 2,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    renderWorkbench();
    const rows = await screen.findAllByTestId("mail-row-148");
    fireEvent.click(rows[1]); // INBOX copy renders second (desc order)
    await waitFor(() => expect(fetchThreadMembers).not.toHaveBeenCalled());
    const callsBefore = vi.mocked(fetchThreadMembers).mock.calls.length;
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [SENT_148],
      total: 1,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    fireEvent.click(screen.getByTestId("webmail-refresh-btn"));
    // Give any erroneous query a chance to fire.
    await waitFor(() => expect(mailboxApi.listMessages).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));
    // No substitution: the members query stays ended (count unchanged) and
    // the last target never switches to another box's copy.
    expect(vi.mocked(fetchThreadMembers).mock.calls.length).toBe(callsBefore);
    const calls = vi.mocked(fetchThreadMembers).mock.calls;
    const lastCall = calls[calls.length - 1];
    if (lastCall) {
      expect(lastCall[0]).not.toBe("Sent");
      expect(lastCall[1]).not.toBe(148);
    }
  });

  it("never fires members for an unverified row that carries a folder_enc", async () => {
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [
        {
          uid: 301,
          subject: "unverified with folder_enc",
          from: [{ name: "M", email: "m@x" }],
          date: "2026-11-06T10:00:00Z",
          flags: [],
          size: 1,
          folder: "INBOX",
          folder_enc: "b64.SU5CT1g",
          thread_verification: "unverified",
        },
      ],
      total: 1,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    renderWorkbench();
    const row = await screen.findByTestId("mail-row-301");
    fireEvent.click(row);
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "b64.SU5CT1g",
        301,
        expect.anything(),
      ),
    );
    expect(fetchThreadMembers).not.toHaveBeenCalled();
  });

  it("highlight follows the folder being read, not the browsing folder", async () => {
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[1]); // read Sent copy
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "Sent",
        148,
        expect.anything(),
      ),
    );
    // The INBOX list row shares the uid but is a different mail.
    expect(screen.getByTestId("mail-row-148").getAttribute("data-active")).toBe(
      "false",
    );
    fireEvent.click(screen.getByTestId("mail-row-148"));
    await waitFor(() =>
      expect(
        screen.getByTestId("mail-row-148").getAttribute("data-active"),
      ).toBe("true"),
    );
  });

  it("keeps the Sent anchor after the list refresh removes its row", async () => {
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[1]); // open Sent copy (row 148 INBOX still listed)
    await waitFor(() => {
      const calls = vi.mocked(fetchThreadMembers).mock.calls;
      expect(calls[calls.length - 1]?.[0]).toBe("Sent");
    });
    // Refresh removes every list row.
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [],
      total: 0,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    fireEvent.click(screen.getByTestId("webmail-refresh-btn"));
    await waitFor(() => {
      const calls = vi.mocked(fetchThreadMembers).mock.calls;
      const last = calls[calls.length - 1];
      expect(last?.[0]).toBe("Sent");
      expect(last?.[1]).toBe(148);
    });
    // Stack context did not collapse.
    expect(screen.getAllByTestId(/^thread-card-\d+$/).length).toBeGreaterThan(
      1,
    );
  });

  it(
    "mixed cross-folder search keeps both same-uid rows and routes Reply/Forward correctly",
    { timeout: 45000 },
    async () => {
      // QA repro: Sent 148 listed BEFORE INBOX 148 — bare-uid dedupe used to
      // erase the INBOX row entirely.
      vi.mocked(mailboxApi.listFolders).mockResolvedValue({
        folders: [
          { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
          { name: "Sent", name_enc: "Sent", delimiter: "/" },
          { name: "Trash", name_enc: "Trash", delimiter: "/" },
        ] as never,
      });
      vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
        messages: [SENT_148, INBOX_148],
        total: 2,
        q: "",
      });
      renderWorkbench();
      // Trigger cross-folder search.
      const search = screen.getByTestId("webmail-search-input");
      fireEvent.change(search, { target: { value: "cross-box" } });
      fireEvent.keyDown(search, { key: "Enter" });
      // Threaded search view groups the two copies into one conversation row
      // ("Ann (2)") — the composite dedupe must NOT have erased the INBOX
      // row from the group (sender summary contains both copies' data).
      const row = await screen.findByTestId("mail-row-148");
      expect(row.textContent).toContain("(2)");
      // Right-click the group row while reading INBOX (contextRow resolves
      // by folder+uid) → Reply targets ann@x with the INBOX subject.
      fireEvent.contextMenu(row);
      const reply = await screen.findByTestId("ctx-reply");
      fireEvent.click(reply);
      const dock = await screen.findByTestId(
        "webmail-compose-dock",
        {},
        {
          timeout: 5000,
        },
      );
      expect(dock.textContent).toContain("ann@x");
      expect(dock.textContent).toContain("Re: cross-box sample");
      // Close, then Forward the same row: subject prefixed correctly.
      fireEvent.click(
        await screen.findByTestId(
          "compose-close-button",
          {},
          { timeout: 5000 },
        ),
      );
      // Re-query the row AFTER the refresh re-render: the old node reference
      // is detached and events on it never reach the list handlers.
      const freshRow = await screen.findByTestId(
        "mail-row-148",
        {},
        { timeout: 5000 },
      );
      fireEvent.contextMenu(freshRow);
      fireEvent.click(
        await screen.findByTestId("ctx-forward", {}, { timeout: 5000 }),
      );
      const dock2 = await screen.findByTestId(
        "webmail-compose-dock",
        {},
        {
          timeout: 5000,
        },
      );
      expect(dock2.textContent).toContain("Fwd: cross-box sample");
    },
  );

  it("keeps the members query anchored when a stack member is not in the list", async () => {
    // Thread carries a Sent member (uid 200) that has NO list row.
    const MEMBER_200 = {
      ...SENT_148,
      uid: 200,
      subject: "Sent-only member",
      thread_id: undefined,
    };
    vi.mocked(fetchThreadMembers).mockResolvedValue({
      thread_id: 148,
      total: 3,
      unread_count: 0,
      scope: "server",
      messages: [INBOX_148, SENT_148, MEMBER_200] as never,
      has_more_older: false,
      oldest: "INBOX:148",
    });
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers200 = await screen.findAllByTestId(/^thread-card-header-200$/);
    fireEvent.click(headers200[0]);
    await waitFor(() =>
      expect(fetchThreadMembers).toHaveBeenLastCalledWith(
        "Sent",
        200,
        expect.anything(),
      ),
    );
    // The stack must still render all three cards (context not lost).
    await waitFor(() =>
      expect(screen.getAllByTestId(/^thread-card-\d+$/)).toHaveLength(3),
    );
  });

  it("survives the list row disappearing after delete and keeps undo context", async () => {
    vi.mocked(mailboxApi.moveMessages).mockResolvedValueOnce({
      moved: 1,
      to: "Trash",
      destination_uids: [900],
      destination_uid_validity: 11,
    } as never);
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    // After the delete, the list no longer contains the INBOX row.
    vi.mocked(mailboxApi.listMessages).mockResolvedValue({
      messages: [],
      total: 0,
      limit: 100,
      offset: 0,
      uid_validity: 7,
    } as never);
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[0]); // expand INBOX row
    fireEvent.click(await screen.findByTestId("thread-card-delete-148"));
    // Relay moves to the Sent copy; anchored via stackOrigin although the
    // INBOX list row is gone.
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "Sent",
        148,
        expect.anything(),
      ),
    );
    const calls = vi.mocked(fetchThreadMembers).mock.calls;
    const last = calls[calls.length - 1];
    expect(last?.[0]).toBe("Sent");
    expect(last?.[1]).toBe(148);
    // Undo context still offered and refreshes members.
    expect(await screen.findByTestId("webmail-move-undo")).toBeTruthy();
  });

  it("keeps remote-image grants box-scoped for the same uid", async () => {
    vi.mocked(mailboxApi.getMessage).mockImplementation(((
      folder: string,
      uid: number,
      opts?: { loadImages?: boolean },
    ) =>
      Promise.resolve({
        uid,
        subject: `detail ${uid}`,
        from: [{ name: "S", email: "s@x" }],
        to: [],
        date: "2026-11-06T10:00:00Z",
        flags: [],
        size: 1,
        html: "<p>with <img src='https://tracker.com/p.png'/></p>",
        notes: opts?.loadImages ? [] : ["remote_images_blocked"],
        attachments: [],
        folder,
      })) as never);
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    // Grant images on the INBOX copy.
    await waitFor(() =>
      expect(screen.getByTestId("show-images-button")).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId("show-images-button"));
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.objectContaining({ loadImages: true }),
      ),
    );
    // Switch to the Sent copy: the grant must NOT carry over.
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[1]);
    await waitFor(() => {
      const sentCalls = vi
        .mocked(mailboxApi.getMessage)
        .mock.calls.filter(([folder]) => folder === "Sent");
      expect(sentCalls.length).toBeGreaterThan(0);
      for (const [, , opts] of sentCalls) {
        expect(
          (opts as { loadImages?: boolean } | undefined)?.loadImages,
        ).not.toBe(true);
      }
    });
  });

  it("a list click while the Sent copy is expanded still opens the INBOX copy", async () => {
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[1]); // expand Sent
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "Sent",
        148,
        expect.anything(),
      ),
    );
    // Query cache may serve the INBOX detail without a network call, so
    // assert the identity state the click produces instead: the INBOX card
    // becomes the expanded one again (Sent card collapses).
    fireEvent.click(screen.getByTestId("mail-row-148"));
    await waitFor(() => {
      const h = screen.getAllByTestId(/^thread-card-header-148$/);
      expect(h[0].getAttribute("aria-expanded")).toBe("true");
      expect(h[1].getAttribute("aria-expanded")).toBe("false");
    });
  });

  it("does not fall back to another box's copy when the browsing folder has no member", async () => {
    // Thread payload only carries the Sent copy; the INBOX row exists in
    // the list. A hint-less list click must stay on the INBOX row identity
    // instead of guessing the Sent member.
    vi.mocked(fetchThreadMembers).mockResolvedValue({
      thread_id: 148,
      total: 1,
      unread_count: 0,
      scope: "folder",
      messages: [SENT_148] as never,
      has_more_older: false,
      oldest: "Sent:148",
    });
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    fireEvent.click(screen.getByTestId("mail-row-148"));
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
    expect(mailboxApi.getMessage).not.toHaveBeenCalledWith(
      "Sent",
      148,
      expect.anything(),
    );
  });

  it("delete relay is bidirectional across the same-uid pair", async () => {
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    let headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    // Expand INBOX, delete it → Sent copy gets relayed in.
    fireEvent.click(headers[0]);
    fireEvent.click(await screen.findByTestId("thread-card-delete-148"));
    await waitFor(() =>
      expect(mailboxApi.moveMessages).toHaveBeenCalledWith(
        "INBOX",
        expect.objectContaining({ uids: [148], to: "Trash" }),
      ),
    );
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "Sent",
        148,
        expect.anything(),
      ),
    );
    // Reverse: expand Sent, delete it → relay back to the INBOX copy:
    // the request target, the expanded state and the keyboard focus must
    // all land on INBOX 148 (same numeric uid, different box).
    headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[1]);
    fireEvent.click(await screen.findByTestId("thread-card-delete-148"));
    await waitFor(() =>
      expect(mailboxApi.moveMessages).toHaveBeenCalledWith(
        "Sent",
        expect.objectContaining({ uids: [148], to: "Trash", uid_validity: 9 }),
      ),
    );
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
    await waitFor(() => {
      const inboxHeader = screen.getAllByTestId(/^thread-card-header-148$/)[0];
      expect(inboxHeader.getAttribute("aria-expanded")).toBe("true");
      expect(document.activeElement).toBe(inboxHeader);
    });
  });

  it("does not offer undo when the destination UIDVALIDITY is missing", async () => {
    vi.mocked(mailboxApi.moveMessages).mockResolvedValueOnce({
      moved: 1,
      to: "Trash",
      destination_uids: [900],
    } as never);
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[0]);
    fireEvent.click(await screen.findByTestId("thread-card-delete-148"));
    await waitFor(() =>
      expect(mailboxApi.moveMessages).toHaveBeenCalledWith(
        "INBOX",
        expect.objectContaining({ uids: [148], to: "Trash" }),
      ),
    );
    expect(screen.queryByTestId("webmail-move-undo")).toBeNull();
  });

  it("undo restores through the destination box and refreshes thread members", async () => {
    vi.mocked(mailboxApi.moveMessages).mockResolvedValueOnce({
      moved: 1,
      to: "Trash",
      destination_uids: [900],
      destination_uid_validity: 11,
    } as never);
    renderWorkbench();
    await screen.findByTestId("mail-row-148");
    const headers = await screen.findAllByTestId(/^thread-card-header-148$/);
    fireEvent.click(headers[0]);
    fireEvent.click(await screen.findByTestId("thread-card-delete-148"));
    const undo = await screen.findByTestId("webmail-move-undo");
    const callsBefore = vi.mocked(fetchThreadMembers).mock.calls.length;
    fireEvent.click(undo);
    await waitFor(() =>
      expect(mailboxApi.moveMessages).toHaveBeenCalledWith("Trash", {
        uids: [900],
        to: "INBOX",
        uid_validity: 11,
      }),
    );
    await waitFor(() =>
      expect(vi.mocked(fetchThreadMembers).mock.calls.length).toBeGreaterThan(
        callsBefore,
      ),
    );
  });
});
