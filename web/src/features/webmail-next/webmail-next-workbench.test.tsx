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
    await waitFor(() => {
      expect(screen.getByTestId("webmail-move-notice")).toBeTruthy();
    });
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
