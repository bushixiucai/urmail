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
      ],
    });
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
  it("refuses stale context reply after INBOX disappears and Sent with the same UID remains", async () => {
    const client = setup();
    await initialSelection();
    const input = screen.getByTestId("webmail-search-input");
    fireEvent.change(input, { target: { value: "origin" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.contextMenu(await screen.findByTestId("mail-row-148"));
    expect(screen.getByTestId("ctx-reply")).toBeTruthy();
    expect(screen.queryByTestId("webmail-compose-dock")).toBeNull();
    vi.mocked(mailboxApi.crossFolderSearch).mockResolvedValue({
      messages: [sent],
      total: 1,
      q: "origin",
    });
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["webmail"] });
    });
    await screen.findByText("Sent same uid");
    expect(screen.getByTestId("mail-row-148").textContent).toContain(
      "Sent same uid",
    );
    fireEvent.click(screen.getByTestId("ctx-reply"));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 500));
    });
    expect(screen.queryByTestId("webmail-compose-dock")).toBeNull();
    expect(screen.queryByTestId("compose-subject-input")).toBeNull();
  });
});
