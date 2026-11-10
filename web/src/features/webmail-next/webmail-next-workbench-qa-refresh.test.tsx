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
const member = {
  ...inbox,
  uid: 149,
  subject: "Sent stack member",
  folder: "Sent",
  folder_enc: "Sent",
  thread_id: undefined,
};
const sent = {
  ...inbox,
  subject: "Foreign Sent origin",
  folder: "Sent",
  folder_enc: "Sent",
  thread_id: 777,
};
const unverified = {
  ...inbox,
  subject: "Unverified INBOX refresh",
  thread_id: undefined,
  thread_count: undefined,
  thread_verification: "unverified",
};
const page = (messages: unknown[]) => ({
  messages,
  total: messages.length,
  limit: 100,
  offset: 0,
  uid_validity: 7,
});

describe("QA refresh lifecycle", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function setup() {
    vi.stubGlobal("innerWidth", 1440);
    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [
        { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
        { name: "Sent", name_enc: "Sent", delimiter: "/" },
      ],
    });
    vi.mocked(mailboxApi.listMessages).mockResolvedValue(
      page([inbox]) as never,
    );
    vi.mocked(mailboxApi.getMessage).mockImplementation(((
      folder: string,
      uid: number,
    ) =>
      Promise.resolve({
        ...inbox,
        uid,
        subject: `${folder} detail`,
        to: [],
        text: `${folder} unique body`,
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
    vi.mocked(fetchThreadMembers).mockResolvedValue({
      thread_id: 148,
      total: 2,
      unread_count: 0,
      scope: "server",
      messages: [inbox, member],
      has_more_older: false,
    } as never);
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
    await waitFor(() =>
      expect(fetchThreadMembers).toHaveBeenCalledWith(
        "INBOX",
        148,
        expect.anything(),
      ),
    );
    await screen.findByTestId("thread-card-149");
    await screen.findByText("INBOX unique body");
    await waitFor(() => expect(client.isFetching()).toBe(0));
    return client;
  }

  async function refresh(client: QueryClient, rows: unknown[], marker: string) {
    const before = vi.mocked(mailboxApi.listMessages).mock.calls.length;
    vi.mocked(mailboxApi.listMessages).mockResolvedValue(page(rows) as never);
    fireEvent.click(screen.getByTestId("webmail-refresh-btn"));
    await waitFor(() => {
      expect(
        vi.mocked(mailboxApi.listMessages).mock.calls.length,
      ).toBeGreaterThan(before);
      expect(screen.getAllByText(marker).length).toBeGreaterThan(0);
    });
    await waitFor(() => expect(client.isFetching()).toBe(0));
  }

  async function invalidate(client: QueryClient) {
    await act(async () => {
      await client.invalidateQueries();
    });
    await waitFor(() => expect(client.isFetching()).toBe(0));
  }

  it.each(["members query", "old thread cards", "original folder body"])(
    "verified to explicit unverified refresh preserves %s",
    async (check) => {
      const client = await setup();
      await refresh(client, [unverified, sent], "Unverified INBOX refresh");
      const count = vi.mocked(fetchThreadMembers).mock.calls.length;
      await invalidate(client);
      if (check === "members query")
        expect(fetchThreadMembers).toHaveBeenCalledTimes(count);
      if (check === "old thread cards")
        expect(screen.queryByTestId("thread-card-149")).toBeNull();
      if (check === "original folder body") {
        expect(screen.getByText("INBOX unique body")).toBeTruthy();
        expect(screen.queryByText("Sent unique body")).toBeNull();
        expect(
          vi
            .mocked(mailboxApi.getMessage)
            .mock.calls.every(
              ([folder, uid]) => uid !== 148 || folder === "INBOX",
            ),
        ).toBe(true);
      }
    },
  );

  it("ends safely when an unverified origin is removed and foreign same UID remains", async () => {
    const client = await setup();
    await refresh(client, [unverified, sent], "Unverified INBOX refresh");
    await refresh(client, [sent], "Foreign Sent origin");
    const count = vi.mocked(fetchThreadMembers).mock.calls.length;
    await invalidate(client);
    expect(fetchThreadMembers).toHaveBeenCalledTimes(count);
    expect(
      vi
        .mocked(fetchThreadMembers)
        .mock.calls.every(([folder, uid]) => folder !== "Sent" || uid !== 148),
    ).toBe(true);
    expect(screen.queryByText("Sent unique body")).toBeNull();
  });

  it("keeps a legal stack member without a list row anchored after invalidation", async () => {
    const client = await setup();
    fireEvent.click(screen.getByTestId("thread-card-header-149"));
    await waitFor(() =>
      expect(fetchThreadMembers).toHaveBeenLastCalledWith(
        "Sent",
        149,
        expect.anything(),
      ),
    );
    await screen.findByText("Sent unique body");
    const count = vi.mocked(fetchThreadMembers).mock.calls.length;
    await invalidate(client);
    expect(vi.mocked(fetchThreadMembers).mock.calls.length).toBeGreaterThan(
      count,
    );
    expect(fetchThreadMembers).toHaveBeenLastCalledWith(
      "Sent",
      149,
      expect.anything(),
    );
    expect(screen.getByTestId("thread-card-149")).toBeTruthy();
  });
});
