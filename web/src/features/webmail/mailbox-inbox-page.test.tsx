import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { MailboxInboxPage } from "./mailbox-inbox-page";

vi.mock("react-i18next", () => {
  // Stable `t` — inbox effects depend on it; a fresh function each render loops forever.
  const t = (k: string, o?: Record<string, unknown>) =>
    o?.count != null ? `${k}:${o.count}` : k;
  const i18n = { language: "en" };
  return {
    useTranslation: () => ({ t, i18n }),
  };
});

vi.mock("./use-mailbox-native", () => ({
  useMailboxNative: () => ({ native: true, loaded: true }),
}));

function sessionStorageReady() {
  window.sessionStorage.setItem("urmail.mailbox-token", "mbox-token");
  window.sessionStorage.setItem("urmail.mailbox-address", "user@example.test");
}

function mockMailboxApi(
  opts: {
    messages?: Array<Record<string, unknown>>;
    folders?: Array<Record<string, unknown>>;
  } = {},
) {
  const folders = opts.folders ?? [
    {
      name: "INBOX",
      name_enc: "b64.SU5CT1g",
      delimiter: "/",
      flags: [],
    },
    {
      name: "Archive",
      name_enc: "b64.QXJjaGl2ZQ",
      delimiter: "/",
      flags: [],
    },
  ];
  const messages = opts.messages ?? [
    {
      uid: 1,
      subject: "Hello inbox",
      from: [{ email: "a@example.test", name: "Alice" }],
      date: "2026-01-02T03:04:05Z",
      flags: [],
      size: 12,
    },
    {
      uid: 2,
      subject: "Second mail",
      from: [{ email: "b@example.test", name: "Bob" }],
      date: "2026-01-03T03:04:05Z",
      flags: ["\\Seen"],
      size: 20,
    },
  ];

  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url);
    if (u === "/api/v1/mailbox/folders") {
      return {
        ok: true,
        status: 200,
        json: async () => ({ folders }),
      };
    }
    if (u.includes("/messages/flags")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          updated: JSON.parse(String(init?.body)).uids.length,
        }),
      };
    }
    if (u.includes("/messages/move")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          moved: JSON.parse(String(init?.body)).uids.length,
          to: JSON.parse(String(init?.body)).to,
        }),
      };
    }
    if (u.includes("/messages/delete")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          deleted: JSON.parse(String(init?.body)).uids.length,
          expunged: true,
        }),
      };
    }
    if (/\/messages\/\d+$/.test(u)) {
      const uid = Number(u.split("/").pop());
      const msg = messages.find((m) => m.uid === uid) ?? messages[0];
      return {
        ok: true,
        status: 200,
        json: async () => ({
          ...msg,
          text: "body",
          attachments: [],
        }),
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

describe("mailbox inbox smoke", () => {
  it("lists folders and messages with mailbox bearer", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi({
      messages: [
        {
          uid: 1,
          subject: "Hello inbox",
          from: [{ email: "a@example.test", name: "Alice" }],
          date: "2026-01-02T03:04:05Z",
          flags: [],
          size: 12,
        },
      ],
      folders: [
        {
          name: "INBOX",
          name_enc: "b64.SU5CT1g",
          delimiter: "/",
          flags: [],
        },
      ],
    });

    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );

    await screen.findByText("user@example.test");
    await screen.findByText("Hello inbox");
    expect(screen.getAllByText("INBOX").length).toBeGreaterThan(0);

    await waitFor(() => {
      const foldersCall = fetcher.mock.calls.find(
        (c) => c[0] === "/api/v1/mailbox/folders",
      ) as [string, RequestInit] | undefined;
      const headers = foldersCall?.[1]?.headers as
        Record<string, string> | undefined;
      expect(headers?.Authorization).toBe("Bearer mbox-token");
    });
  });
});

describe("mailbox inbox bulk selection (W4)", () => {
  it("select-all then mark read posts flags and clears selection", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi();

    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );

    await screen.findByText("Hello inbox");
    await screen.findByText("Second mail");

    fireEvent.click(screen.getByLabelText("page.webmail.select_all"));

    expect(
      await screen.findByText("page.webmail.selected_count:2"),
    ).toBeTruthy();

    fireEvent.click(
      screen.getByRole("button", { name: /page\.webmail\.mark_read/ }),
    );

    await waitFor(() => {
      const flagsCall = fetcher.mock.calls.find((c) =>
        String(c[0]).includes("/messages/flags"),
      ) as [string, RequestInit] | undefined;
      expect(flagsCall).toBeTruthy();
      const body = JSON.parse(String(flagsCall?.[1]?.body));
      expect(body.uids).toEqual([1, 2]);
      expect(body.add).toEqual(["\\Seen"]);
    });

    await waitFor(() => {
      expect(screen.queryByText("page.webmail.selected_count:2")).toBeNull();
    });
  });

  it("bulk move uses selected uids then clears selection", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi();

    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );

    await screen.findByText("Hello inbox");

    const rowChecks = screen.getAllByLabelText(
      "page.webmail.select_message_row",
    );
    fireEvent.click(rowChecks[0]);

    const toolbar = screen.getByRole("toolbar", {
      name: "page.webmail.bulk_actions",
    });
    const moveSelect = within(toolbar).getByLabelText("page.webmail.move_to");
    fireEvent.change(moveSelect, {
      target: { value: "b64.QXJjaGl2ZQ" },
    });
    fireEvent.click(
      within(toolbar).getByRole("button", { name: "page.webmail.move" }),
    );

    await waitFor(() => {
      const moveCall = fetcher.mock.calls.find((c) =>
        String(c[0]).includes("/messages/move"),
      ) as [string, RequestInit] | undefined;
      expect(moveCall).toBeTruthy();
      const body = JSON.parse(String(moveCall?.[1]?.body));
      expect(body.uids).toEqual([1]);
      expect(body.to).toBe("b64.QXJjaGl2ZQ");
    });

    await waitFor(() => {
      expect(screen.queryByRole("toolbar")).toBeNull();
    });
  });

  it("bulk delete confirms and posts delete for checked uids", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi();

    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );

    await screen.findByText("Hello inbox");
    fireEvent.click(screen.getByLabelText("page.webmail.select_all"));

    const toolbar = screen.getByRole("toolbar", {
      name: "page.webmail.bulk_actions",
    });
    fireEvent.click(
      within(toolbar).getByRole("button", { name: /page\.webmail\.delete/ }),
    );

    expect(
      await screen.findByText("page.webmail.bulk_delete_confirm_title"),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "action.confirm" }));

    await waitFor(() => {
      const delCall = fetcher.mock.calls.find((c) =>
        String(c[0]).includes("/messages/delete"),
      ) as [string, RequestInit] | undefined;
      expect(delCall).toBeTruthy();
      const body = JSON.parse(String(delCall?.[1]?.body));
      expect(body.uids).toEqual([1, 2]);
      expect(body.expunge).toBe(true);
    });
  });
});

describe("mailbox inbox mobile panes (W5)", () => {
  it("opens detail with back control and folders menu trigger", async () => {
    sessionStorageReady();
    mockMailboxApi();

    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );

    await screen.findByText("Hello inbox");
    expect(screen.getByLabelText("page.webmail.folders_menu")).toBeTruthy();

    fireEvent.click(screen.getByText("Hello inbox"));

    expect(await screen.findByText("body")).toBeTruthy();
    expect(screen.getByLabelText("page.webmail.back_to_list")).toBeTruthy();

    fireEvent.click(screen.getByLabelText("page.webmail.back_to_list"));
    // List + detail both keep the subject in the DOM (detail is CSS-hidden on narrow).
    await waitFor(() => {
      expect(screen.getAllByText("Hello inbox").length).toBeGreaterThanOrEqual(
        1,
      );
    });
    expect(screen.getByLabelText("page.webmail.select_all")).toBeTruthy();
  });
});

describe("mailbox asynchronous isolation", () => {
  it("does not apply a delayed search response after switching folders", async () => {
    sessionStorageReady();
    const base = mockMailboxApi();
    let finishSearch!: (value: unknown) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        const u = String(url);
        if (u.includes("q=delayed"))
          return new Promise((resolve) => {
            finishSearch = resolve;
          });
        return base(url, init);
      }),
    );
    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );
    await screen.findByText("Hello inbox");
    fireEvent.change(screen.getByLabelText("page.webmail.search_aria"), {
      target: { value: "delayed" },
    });
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() => expect(finishSearch).toBeTypeOf("function"));
    fireEvent.click(screen.getByRole("button", { name: "Archive" }));
    await screen.findByText("Hello inbox");
    await act(async () => {
      finishSearch({
        ok: true,
        status: 200,
        json: async () => ({
          messages: [
            {
              uid: 99,
              subject: "STALE SEARCH",
              from: [],
              date: "",
              flags: [],
              size: 0,
            },
          ],
          total: 1,
          offset: 0,
          limit: 50,
        }),
      });
    });
    expect(screen.queryByText("STALE SEARCH")).toBeNull();
    expect(screen.getByText("Hello inbox")).toBeTruthy();
  });

  it("keeps the original draft when an attachment cannot be restored", async () => {
    sessionStorageReady();
    const base = mockMailboxApi({
      folders: [
        {
          name: "Drafts",
          name_enc: "drafts",
          delimiter: "/",
          flags: ["\\Drafts"],
        },
      ],
    });
    const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url);
      if (u.includes("/attachments/"))
        return {
          ok: false,
          status: 503,
          json: async () => ({
            error: {
              code: "IMAP_UNAVAILABLE",
              message: "error.IMAP_UNAVAILABLE",
            },
          }),
        };
      if (/\/messages\/\d+$/.test(u))
        return {
          ok: true,
          status: 200,
          json: async () => ({
            uid: 1,
            uid_validity: 9,
            subject: "Draft body",
            from: [],
            date: "",
            flags: [],
            size: 0,
            text: "original",
            attachments: [
              {
                part_id: "2",
                filename: "important.bin",
                content_type: "application/octet-stream",
                size: 10,
              },
            ],
          }),
        };
      return base(url, init);
    });
    vi.stubGlobal("fetch", fetcher);
    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByText("Hello inbox"));
    fireEvent.click(
      await screen.findByRole("button", {
        name: "page.webmail.compose_edit_draft",
      }),
    );
    await screen.findByRole("alert");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(
      fetcher.mock.calls.some(
        ([, init]) => init?.method === "PUT" || init?.method === "DELETE",
      ),
    ).toBe(false);
  });
});

it("does not reset compose contents when the inbox receives the saved draft UID", async () => {
  sessionStorageReady();
  const base = mockMailboxApi();
  const savedBodies: Array<Record<string, unknown>> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url === "/api/v1/mailbox/drafts" && init?.method === "PUT") {
        savedBodies.push(JSON.parse(String(init.body)));
        return {
          ok: true,
          status: 200,
          json: async () => ({
            saved: true,
            uid: 77 + savedBodies.length,
            uid_validity: 9,
            folder: "Drafts",
            name_enc: "drafts",
          }),
        };
      }
      return base(url, init);
    }),
  );
  render(
    <MemoryRouter>
      <MailboxInboxPage />
    </MemoryRouter>,
  );
  await screen.findByText("Hello inbox");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_new" }),
  );
  fireEvent.change(screen.getByLabelText("page.webmail.compose_to"), {
    target: { value: "bob@example.test" },
  });
  fireEvent.change(screen.getByLabelText("page.webmail.compose_body"), {
    target: { value: "Keep current input" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
  );
  await screen.findByText("page.webmail.compose_draft_saved");
  expect(
    (screen.getByLabelText("page.webmail.compose_body") as HTMLTextAreaElement)
      .value,
  ).toBe("Keep current input");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
  );
  await waitFor(() => expect(savedBodies).toHaveLength(2));
  expect(savedBodies[1]).toMatchObject({
    uid: 78,
    uid_validity: 9,
    text: "Keep current input",
  });
});
