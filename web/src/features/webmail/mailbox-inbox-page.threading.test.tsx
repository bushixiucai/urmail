import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { MailboxInboxPage } from "./mailbox-inbox-page";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o?.count != null ? k + ":" + o.count : k;
  const i18n = { language: "en" };
  return { useTranslation: () => ({ t, i18n }) };
});

vi.mock("./use-mailbox-native", () => ({
  useMailboxNative: () => ({ native: true, loaded: true }),
}));

function sessionStorageReady() {
  window.sessionStorage.setItem("urmail.mailbox-token", "mbox-token");
  window.sessionStorage.setItem("urmail.mailbox-address", "user@example.test");
}

interface Row {
  uid: number;
  subject: string;
  from?: Array<{ email: string }>;
  date?: string;
  flags?: string[];
  size?: number;
}

function mockMailboxApi(opts: { messages?: Row[] } = {}) {
  const folders = [
    { name: "INBOX", name_enc: "b64.SU5CT1g", delimiter: "/", flags: [] },
    { name: "Sent", name_enc: "b64.U2VudA", delimiter: "/", flags: [] },
  ];
  const messages = opts.messages ?? [
    {
      uid: 1,
      subject: "Plan",
      from: [{ email: "a@example.test" }],
      date: "2026-01-01T00:00:00Z",
      flags: [],
      size: 1,
    },
    {
      uid: 2,
      subject: "Re: Plan",
      from: [{ email: "b@example.test" }],
      date: "2026-01-02T00:00:00Z",
      flags: ["\\Seen"],
      size: 2,
    },
    {
      uid: 3,
      subject: "Unrelated",
      from: [{ email: "c@example.test" }],
      date: "2026-01-03T00:00:00Z",
      flags: ["\\Seen"],
      size: 3,
    },
  ];
  const fetcher = vi.fn(async (url: string) => {
    const u = String(url);
    if (u === "/api/v1/mailbox/folders") {
      return { ok: true, status: 200, json: async () => ({ folders }) };
    }
    if (u.startsWith("/api/v1/mailbox/search?")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          messages: [
            {
              uid: 9,
              subject: "Cross hit",
              from: [{ email: "z@x" }],
              date: "2026-02-01T00:00:00Z",
              flags: [],
              size: 9,
              folder: "Sent",
              folder_enc: "b64.U2VudA",
            },
          ],
          total: 1,
          q: "cross",
        }),
      };
    }
    if (/\/messages\/\d+$/.test(u)) {
      const uid = Number(u.split("/").pop());
      const msg = messages.find((m) => m.uid === uid) ?? messages[0];
      return {
        ok: true,
        status: 200,
        json: async () => ({ ...msg, text: "body", attachments: [] }),
      };
    }
    if (u.includes("/messages")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          messages,
          total: messages.length,
          limit: 50,
          offset: 0,
        }),
      };
    }
    return { ok: false, status: 404, json: async () => ({}) };
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

function renderInbox() {
  return render(
    <MemoryRouter>
      <MailboxInboxPage />
    </MemoryRouter>,
  );
}

describe("mailbox inbox thread grouping (WEBMAIL-004)", () => {
  it("shows a thread badge and filters to the conversation on click", async () => {
    sessionStorageReady();
    mockMailboxApi();
    renderInbox();
    await screen.findByText("Plan");
    fireEvent.click(screen.getByLabelText("page.webmail.thread_group"));
    await waitFor(() => {
      // Both rows of the conversation carry the badge.
      expect(
        screen.getAllByLabelText("page.webmail.thread_filter:2"),
      ).toHaveLength(2);
    });
    expect(screen.getByText("Plan")).toBeTruthy();
    fireEvent.click(
      screen.getAllByLabelText("page.webmail.thread_filter:2")[0],
    );
    await waitFor(() => {
      expect(screen.queryByText("Unrelated")).toBeNull();
    });
    expect(screen.getByText("Plan")).toBeTruthy();
    expect(screen.getByText("Re: Plan")).toBeTruthy();
    fireEvent.click(screen.getByText("page.webmail.thread_show_all"));
    await waitFor(() => {
      expect(screen.getByText("Unrelated")).toBeTruthy();
    });
  });
});

describe("mailbox inbox cross-folder search entry (WEBMAIL-004)", () => {
  it("fans the search out to /mailbox/search when the scope is enabled", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi();
    renderInbox();
    await screen.findByText("Plan");
    fireEvent.click(screen.getByLabelText("page.webmail.search_all_folders"));
    const box = screen.getByLabelText(
      "page.webmail.search_aria",
    ) as HTMLInputElement;
    fireEvent.change(box, { target: { value: "cross" } });
    fireEvent.submit(box.form as HTMLFormElement);
    await waitFor(() => {
      const call = fetcher.mock.calls.find((c) =>
        String(c[0]).startsWith("/api/v1/mailbox/search?"),
      );
      expect(call).toBeTruthy();
      expect(String(call?.[0])).toContain("q=cross");
    });
    await screen.findByText("Cross hit");
    // Folder tag on the cross-folder hit (plus the folder pane entry).
    expect(screen.getAllByText("Sent").length).toBeGreaterThanOrEqual(2);
  });
});
