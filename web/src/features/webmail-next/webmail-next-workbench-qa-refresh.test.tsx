import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  act,
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

describe("WebmailNextWorkbench automatic selection refresh", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it.each(["same-identity-unverified"])(
    "checks automatic selection after %s refresh",
    async (scenario) => {
      vi.stubGlobal("innerWidth", 1440);
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
      const inboxMember = {
        ...inbox,
        uid: 149,
        subject: "Inbox thread member",
      };
      const sent = {
        ...inbox,
        subject: "Foreign Sent origin",
        folder: "Sent",
        folder_enc: "Sent",
        thread_id: 777,
      };
      const sentMember = { ...sent, uid: 888, subject: "Foreign Sent member" };
      const page = (messages: (typeof inbox)[]) => ({
        messages,
        total: messages.length,
        limit: 100,
        offset: 0,
        uid_validity: 7,
      });
      vi.mocked(mailboxApi.listFolders).mockResolvedValue({
        folders: [
          { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
          { name: "Sent", name_enc: "Sent", delimiter: "/" },
        ],
      });
      vi.mocked(mailboxApi.listMessages).mockResolvedValue(page([inbox]));
      vi.mocked(mailboxApi.getMessage).mockImplementation(((
        _folder: string,
        uid: number,
      ) =>
        Promise.resolve({
          ...inbox,
          uid,
          to: [],
          html: "<p>body</p>",
          notes: [],
          attachments: [],
        })) as never);
      vi.mocked(mailboxApi.getMailboxQuota).mockRejectedValue(
        new Error("quota"),
      );
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
        messages: [inbox, inboxMember],
        has_more_older: false,
      } as never);
      const queryClient = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      });
      render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter>
            <WebmailNextWorkbench />
          </MemoryRouter>
        </QueryClientProvider>,
      );

      // No row click: desktop auto-selection must fetch the verified INBOX origin.
      await waitFor(() =>
        expect(fetchThreadMembers).toHaveBeenCalledWith(
          "INBOX",
          148,
          expect.anything(),
        ),
      );
      expect(await screen.findByTestId("thread-card-149")).toBeTruthy();
      const listCallsBefore = vi.mocked(mailboxApi.listMessages).mock.calls
        .length;
      const membersCallsBefore =
        vi.mocked(fetchThreadMembers).mock.calls.length;
      const unverified = {
        ...inbox,
        subject: "Same identity unverified refresh",
        thread_id: undefined,
        thread_count: undefined,
      };
      vi.mocked(mailboxApi.listMessages).mockResolvedValue(
        page(
          scenario === "foreign" ? [sent, sentMember] : ([unverified] as never),
        ),
      );
      fireEvent.click(screen.getByTestId("webmail-refresh-btn"));
      await waitFor(() => {
        expect(
          vi.mocked(mailboxApi.listMessages).mock.calls.length,
        ).toBeGreaterThan(listCallsBefore);
        expect(
          screen.getAllByText(
            scenario === "foreign"
              ? "Foreign Sent origin"
              : "Same identity unverified refresh",
          ).length,
        ).toBeGreaterThan(0);
      });
      await waitFor(() => expect(queryClient.isFetching()).toBe(0));
      // Invalidate after the new list is rendered to exercise any still-active members query.
      await act(async () => {
        await queryClient.invalidateQueries();
      });
      await waitFor(() => expect(queryClient.isFetching()).toBe(0));
      if (scenario === "same-identity-unverified") {
        expect(vi.mocked(fetchThreadMembers).mock.calls.length).toBe(
          membersCallsBefore,
        );
      }
      expect(
        vi
          .mocked(fetchThreadMembers)
          .mock.calls.every(
            ([folder, uid]) => folder === "INBOX" && uid === 148,
          ),
      ).toBe(true);
      expect(screen.queryByTestId("thread-card-888")).toBeNull();
    },
  );
});
