import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import * as mailboxApi from "@/features/webmail/mailbox-api";
import { fetchThreadMembers } from "./lib/thread-members-api";
import { WebmailNextWorkbench } from "./webmail-next-workbench";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => ({
  ...(await importOriginal<typeof mailboxApi>()),
  listFolders: vi.fn(),
  listMessages: vi.fn(),
  crossFolderSearch: vi.fn(),
  getMessage: vi.fn(),
  getMailboxSettings: vi.fn(),
  getMailboxRules: vi.fn(),
  getMailboxQuota: vi.fn(),
  listScheduledSends: vi.fn(),
  moveMessages: vi.fn(),
  flagMessages: vi.fn(),
  createMailboxFolder: vi.fn(),
}));
vi.mock("./lib/thread-members-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./lib/thread-members-api")>()),
  fetchThreadMembers: vi.fn(),
}));

const inbox = {
  uid: 148,
  subject: "Inbox origin",
  from: [{ name: "Sender", email: "s@x" }],
  date: "2026-11-06T10:00:00Z",
  flags: [],
  size: 1,
  folder: "INBOX",
  folder_enc: "INBOX",
  thread_id: 148,
  thread_count: 2,
};
const { folder: _folder, folder_enc: _folderEnc, ...inboxListRow } = inbox;
const inboxMember = { ...inbox, uid: 149, subject: "Inbox member" };
export const unrelated = {
  ...inboxListRow,
  uid: 201,
  subject: "Unrelated inbox",
  thread_id: 201,
};
const sent = {
  ...inbox,
  subject: "Sent same uid",
  from: [{ name: "Other sender", email: "other@x" }],
  folder: "Sent",
  folder_enc: "Sent",
  thread_id: 777,
};
const sentMember = { ...sent, uid: 888, subject: "Sent member" };
const page = (messages: (typeof inboxListRow)[]) => ({
  messages,
  total: messages.length,
  limit: 50,
  offset: 0,
  uid_validity: 7,
});

describe("automatic INBOX origin on refresh", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  function setup() {
    vi.stubGlobal("innerWidth", 1440);
    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [
        { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
        { name: "Sent", name_enc: "Sent", delimiter: "/" },
        {
          name: "Archive",
          name_enc: "Archive",
          delimiter: "/",
          special_use: "\\Archive",
        },
        {
          name: "Trash",
          name_enc: "Trash",
          delimiter: "/",
          special_use: "\\Trash",
        },
        { name: "Projects", name_enc: "Projects", delimiter: "/" },
        // QA fixtures carry special_use; MailboxFolder does not type it.
      ] as never,
    });
    vi.mocked(mailboxApi.moveMessages).mockResolvedValue({} as never);
    vi.mocked(mailboxApi.flagMessages).mockResolvedValue({} as never);
    vi.mocked(mailboxApi.createMailboxFolder).mockResolvedValue({} as never);
    vi.mocked(mailboxApi.listMessages).mockResolvedValue(page([inboxListRow]));
    vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
      messages: [inbox],
      total: 1,
      q: "origin",
    });
    vi.mocked(mailboxApi.getMessage).mockImplementation(((
      folder: string,
      uid: number,
    ) =>
      Promise.resolve({
        ...(folder === "Sent" ? sent : inbox),
        uid,
        to: [],
        html: `<p>${folder} body ${uid}</p>`,
        text: `${folder} body ${uid}`,
        notes: [],
        attachments: [],
      })) as never);
    vi.mocked(mailboxApi.getMailboxQuota).mockRejectedValue(new Error("quota"));
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
      display_name: "User",
      signature_text: "",
      signature_html: "",
    } as never);
    vi.mocked(mailboxApi.getMailboxRules).mockResolvedValue({
      rules: [],
    } as never);
    vi.mocked(mailboxApi.listScheduledSends).mockResolvedValue({ items: [] });
    vi.mocked(fetchThreadMembers).mockImplementation(((
      folder: string,
      uid: number,
    ) =>
      Promise.resolve({
        thread_id: uid,
        total: 2,
        unread_count: 0,
        scope: "server",
        messages: folder === "Sent" ? [sent, sentMember] : [inbox, inboxMember],
        has_more_older: false,
      })) as never);
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <WebmailNextWorkbench />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    return client;
  }
  async function initialSelection() {
    await waitFor(() =>
      expect(fetchThreadMembers).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
    await screen.findByTestId("thread-card-149");
    await waitFor(() =>
      expect(mailboxApi.getMessage).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
    await waitFor(() =>
      expect(
        document.querySelector("iframe")?.getAttribute("srcdoc"),
      ).toContain("INBOX body 148"),
    );
  }
  it.each(["archive", "delete", "toggle-read", "move"])(
    "stale INBOX %s",
    async (action) => {
      const client = setup();
      await initialSelection();
      const { thread_id: _i, thread_count: _ic, ...searchInbox } = inbox;
      const { thread_id: _s, thread_count: _sc, ...searchSent } = sent;
      vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
        messages: [searchInbox, searchSent],
        total: 2,
        q: "origin",
      });
      const input = screen.getByTestId("webmail-search-input");
      fireEvent.change(input, { target: { value: "origin" } });
      fireEvent.keyDown(input, { key: "Enter" });
      await waitFor(() =>
        expect(screen.getAllByTestId("mail-row-148")).toHaveLength(2),
      );
      const row = screen
        .getAllByTestId("mail-row-148")
        .find((r) => r.textContent?.includes("Inbox origin"));
      expect(row).toBeTruthy();
      fireEvent.contextMenu(row!);
      expect(screen.getByTestId("ctx-archive")).toBeTruthy();
      vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
        messages: [searchSent],
        total: 1,
        q: "origin",
      });
      await act(async () => {
        await client.invalidateQueries({ queryKey: ["webmail"] });
      });
      await waitFor(() => {
        expect(screen.getAllByTestId("mail-row-148")).toHaveLength(1);
        expect(screen.getByTestId("mail-row-148").textContent).toContain(
          "Sent same uid",
        );
      });
      const menu = screen.queryByTestId(`ctx-${action}`);
      console.log(
        "QA sequence",
        action,
        "two folder rows -> INBOX context -> refresh Sent only -> menu",
        !!menu,
      );
      if (menu) {
        fireEvent.click(menu);
        if (action === "move" && screen.queryByTestId("ctx-move-Projects"))
          fireEvent.click(screen.getByTestId("ctx-move-Projects"));
        await act(async () => {
          await new Promise((resolve) => setTimeout(resolve, 50));
        });
      }
      console.log(
        "QA mutations",
        action,
        JSON.stringify({
          move: vi.mocked(mailboxApi.moveMessages).mock.calls,
          flag: vi.mocked(mailboxApi.flagMessages).mock.calls,
          create: vi.mocked(mailboxApi.createMailboxFolder).mock.calls,
          menuClosed: !screen.queryByTestId("ctx-archive"),
        }),
      );
      expect(mailboxApi.moveMessages).not.toHaveBeenCalled();
      expect(mailboxApi.flagMessages).not.toHaveBeenCalled();
      expect(mailboxApi.createMailboxFolder).not.toHaveBeenCalled();
    },
  );
  it("probes disabled gate fallback with compatibility search thread metadata", async () => {
    const client = setup();
    await initialSelection();
    vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
      messages: [inbox, sent, sentMember],
      total: 3,
      q: "origin",
    });
    const input = screen.getByTestId("webmail-search-input");
    fireEvent.change(input, { target: { value: "origin" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() =>
      expect(screen.getAllByTestId("mail-row-148")).toHaveLength(2),
    );
    vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
      messages: [sent, sentMember],
      total: 2,
      q: "origin",
    });
    vi.mocked(fetchThreadMembers).mockClear();
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["webmail", "messages"] });
    });
    await waitFor(() =>
      expect(screen.getAllByTestId("mail-row-148")).toHaveLength(1),
    );
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    console.log(
      "QA fallback",
      JSON.stringify({
        calls: vi.mocked(fetchThreadMembers).mock.calls,
        oldCard149: !!screen.queryByTestId("thread-card-149"),
        sentCard148: !!screen.queryByTestId("thread-card-148"),
        sentCard888: !!screen.queryByTestId("thread-card-888"),
      }),
    );
    expect(fetchThreadMembers).not.toHaveBeenCalled();
    expect(screen.queryByTestId("thread-card-149")).toBeNull();
    const star = screen.queryByTestId("thread-card-star-148");
    console.log("QA fallback star available", !!star);
    if (star) {
      fireEvent.click(star);
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
      });
      console.log(
        "QA fallback star requests",
        JSON.stringify({
          flag: vi.mocked(mailboxApi.flagMessages).mock.calls,
          members: vi.mocked(fetchThreadMembers).mock.calls,
        }),
      );
    }
  });
});
