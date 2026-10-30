import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter as Router } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
function MemoryRouter({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <Router>{children}</Router>
    </QueryClientProvider>
  );
}
import { MailboxInboxPage } from "./mailbox-inbox-page";

vi.mock("@/components/appearance-controls", () => ({
  AppearanceControls: () => null,
}));
vi.mock("react-i18next", () => {
  // Stable `t` — inbox effects depend on it; a fresh function each render loops forever.
  const t = (k: string, o?: Record<string, unknown>) =>
    o?.count != null ? `${k}:${o.count}` : k;
  const i18n = { language: "en", exists: () => true };
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
    if (u === "/api/v1/mailbox/settings")
      return {
        ok: true,
        status: 200,
        json: async () => ({
          address: "user@example.test",
          display_name: "",
          signature_text: "",
          signature_html: "",
          preferences: {},
          version: 1,
        }),
      };
    if (u === "/api/v1/mailbox/identities")
      return {
        ok: true,
        status: 200,
        json: async () => ({
          identities: [{ address: "user@example.test", primary: true }],
        }),
      };
    if (u === "/api/v1/mailbox/calendars/reminders")
      return { ok: true, status: 200, json: async () => ({ reminders: [] }) };
    if (u.startsWith("/api/v1/mailbox/calendars/invitations?"))
      return {
        ok: false,
        status: 404,
        json: async () => ({
          error: { code: "CALENDAR_INVITATION_NOT_FOUND" },
        }),
      };

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
    if (u.includes("/messages/trash") || u.includes("/messages/restore")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          results: JSON.parse(String(init?.body)).uids.map((uid: number) => ({
            uid,
            status: "success",
          })),
          destination: u.includes("/restore") ? "INBOX" : "Trash",
          name_enc: "b64.SU5CT1g",
          uid_validity: 10,
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
    if (/\/messages\/\d+(?:\?|$)/.test(u)) {
      const uid = Number(
        new URL(u, "http://local.test").pathname.split("/").pop(),
      );
      const msg = messages.find((m) => m.uid === uid) ?? messages[0];
      return {
        ok: true,
        status: 200,
        json: async () => ({
          ...msg,
          uid_validity: 9,
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
          uid_validity: 9,
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
      "page.webmail.select_message_named",
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

  it("bulk delete moves to recoverable Trash with generation and durable intent", async () => {
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
      await screen.findByText("page.webmail.trash_confirm_title"),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "action.confirm" }));

    await waitFor(() => {
      const delCall = fetcher.mock.calls.find((c) =>
        String(c[0]).includes("/messages/trash"),
      ) as [string, RequestInit] | undefined;
      expect(delCall).toBeTruthy();
      const body = JSON.parse(String(delCall?.[1]?.body));
      expect(body.uids).toEqual([1, 2]);
      expect(body.expunge).toBeUndefined();
      expect(body.uid_validity).toBe(9);
      expect(
        (delCall?.[1]?.headers as Record<string, string>)["Idempotency-Key"],
      ).toBeTruthy();
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
        if (u === "/api/v1/mailbox/calendars/reminders")
          return {
            ok: true,
            status: 200,
            json: async () => ({ reminders: [] }),
          };
        if (u.startsWith("/api/v1/mailbox/calendars/invitations?"))
          return {
            ok: false,
            status: 404,
            json: async () => ({
              error: { code: "CALENDAR_INVITATION_NOT_FOUND" },
            }),
          };

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
      if (u === "/api/v1/mailbox/calendars/reminders")
        return { ok: true, status: 200, json: async () => ({ reminders: [] }) };
      if (u.startsWith("/api/v1/mailbox/calendars/invitations?"))
        return {
          ok: false,
          status: 404,
          json: async () => ({
            error: { code: "CALENDAR_INVITATION_NOT_FOUND" },
          }),
        };

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
      if (/\/messages\/\d+(?:\?|$)/.test(u))
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
  fireEvent.change(await screen.findByLabelText("page.webmail.compose_to"), {
    target: { value: "bob@example.test" },
  });
  const visibleEditor = await screen.findByRole("textbox", {
    name: "page.webmail.compose_rich_text",
  });
  visibleEditor.innerHTML = "<p>Keep current input</p>";
  fireEvent.input(visibleEditor);
  await waitFor(() =>
    expect(
      (
        screen.getByLabelText(
          "page.webmail.compose_body",
        ) as HTMLTextAreaElement
      ).value,
    ).toBe("Keep current input"),
  );
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.compose_save_draft",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
  );
  await screen.findByText("page.webmail.compose_draft_saved");
  expect(
    (screen.getByLabelText("page.webmail.compose_body") as HTMLTextAreaElement)
      .value,
  ).toBe("Keep current input");
  visibleEditor.innerHTML = "<p>Keep newer input</p>";
  fireEvent.input(visibleEditor);
  await waitFor(() =>
    expect(
      (
        screen.getByLabelText(
          "page.webmail.compose_body",
        ) as HTMLTextAreaElement
      ).value,
    ).toBe("Keep newer input"),
  );
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.compose_save_draft",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
  );
  await waitFor(() => expect(savedBodies).toHaveLength(2));
  expect(savedBodies[1]).toMatchObject({
    uid: 78,
    uid_validity: 9,
    text: "Keep newer input",
  });
});

it("refuses an over-budget restored draft without dropping any existing attachment", async () => {
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
  const bytes = new Uint8Array(5 * 1024 * 1024);
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    const u = String(url);
    if (u === "/api/v1/mailbox/calendars/reminders")
      return { ok: true, status: 200, json: async () => ({ reminders: [] }) };
    if (u.startsWith("/api/v1/mailbox/calendars/invitations?"))
      return {
        ok: false,
        status: 404,
        json: async () => ({
          error: { code: "CALENDAR_INVITATION_NOT_FOUND" },
        }),
      };

    if (u.includes("/attachments/"))
      return {
        ok: true,
        status: 200,
        arrayBuffer: async () =>
          u.includes("/attachments/3")
            ? new Uint8Array(1).buffer
            : bytes.buffer,
      };
    if (/\/messages\/\d+(?:\?|$)/.test(u))
      return {
        ok: true,
        status: 200,
        json: async () => ({
          uid: 1,
          uid_validity: 9,
          subject: "Original draft",
          from: [],
          date: "",
          flags: ["\\Seen"],
          size: 0,
          text: "original",
          attachments: ["1", "2", "3"].map((part_id) => ({
            part_id,
            filename: `${part_id}.bin`,
            content_type: "application/octet-stream",
            size: 1,
          })),
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
  const alert = await screen.findByRole("alert", {}, { timeout: 3000 });
  expect(alert.textContent).toContain("error.MAILBOX_MESSAGE_TOO_LARGE");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    fetcher.mock.calls.filter(([url]) => url.includes("/attachments/")),
  ).toHaveLength(3);
  expect(
    fetcher.mock.calls.some(
      ([, init]) => init?.method === "PUT" || init?.method === "DELETE",
    ),
  ).toBe(false);
});

it("restores every original attachment before opening a forward", async () => {
  sessionStorageReady();
  const base = mockMailboxApi();
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (/\/messages\/1(?:\?|$)/.test(url))
      return {
        ok: true,
        status: 200,
        json: async () => ({
          uid: 1,
          uid_validity: 9,
          subject: "Original with files",
          from: [{ email: "sender@example.test" }],
          date: "",
          flags: ["\\Seen"],
          size: 1,
          text: "Original body",
          attachments: [
            {
              part_id: "2",
              filename: "first.bin",
              content_type: "application/octet-stream",
              size: 1,
            },
            {
              part_id: "3",
              filename: "second.bin",
              content_type: "application/octet-stream",
              size: 1,
            },
          ],
        }),
      };
    if (url.includes("/attachments/"))
      return {
        ok: true,
        status: 200,
        arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer,
      };
    return base(url, init);
  });
  vi.stubGlobal("fetch", fetcher);
  render(
    <MemoryRouter>
      <MailboxInboxPage />
    </MemoryRouter>,
  );
  await screen.findByText("Hello inbox");
  fireEvent.click(screen.getByText("Hello inbox"));
  await screen.findByRole("heading", { name: "Original with files" });
  fireEvent.click(screen.getByText("page.webmail.message_actions"));
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_forward" }),
  );
  const dialog = await screen.findByRole("dialog");
  expect(await within(dialog).findByText("first.bin")).toBeTruthy();
  expect(await within(dialog).findByText("second.bin")).toBeTruthy();
  expect(
    (
      within(dialog).getByLabelText(
        "page.webmail.compose_subject",
      ) as HTMLInputElement
    ).value,
  ).toBe("Fwd: Original with files");
  const parts = fetcher.mock.calls.filter(([url]) =>
    url.includes("/attachments/"),
  );
  expect(parts).toHaveLength(2);
  expect(parts.every(([url]) => url.endsWith("?uid_validity=9"))).toBe(true);
});
it("does not open an incomplete forward when any original attachment fails", async () => {
  sessionStorageReady();
  const base = mockMailboxApi();
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (/\/messages\/1(?:\?|$)/.test(url))
      return {
        ok: true,
        status: 200,
        json: async () => ({
          uid: 1,
          uid_validity: 9,
          subject: "Original with files",
          from: [],
          date: "",
          flags: ["\\Seen"],
          size: 1,
          text: "Original body",
          attachments: [
            {
              part_id: "2",
              filename: "first.bin",
              content_type: "application/octet-stream",
              size: 1,
            },
            {
              part_id: "3",
              filename: "second.bin",
              content_type: "application/octet-stream",
              size: 1,
            },
          ],
        }),
      };
    if (url.includes("/attachments/2"))
      return {
        ok: true,
        status: 200,
        arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer,
      };
    if (url.includes("/attachments/3"))
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
    return base(url, init);
  });
  vi.stubGlobal("fetch", fetcher);
  render(
    <MemoryRouter>
      <MailboxInboxPage />
    </MemoryRouter>,
  );
  await screen.findByText("Hello inbox");
  fireEvent.click(screen.getByText("Hello inbox"));
  await screen.findByRole("heading", { name: "Original with files" });
  fireEvent.click(screen.getByText("page.webmail.message_actions"));
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_forward" }),
  );
  await screen.findByRole("alert");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    fetcher.mock.calls.some(
      ([, init]) =>
        init?.method === "PUT" ||
        String(init?.body ?? "").includes('"attachments"'),
    ),
  ).toBe(false);
});

describe("mailbox source identity regressions", () => {
  function renderMailbox() {
    return render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );
  }
  function response(value: unknown, status = 200) {
    return { ok: status < 400, status, json: async () => value };
  }
  async function searchAll(q = "cross") {
    fireEvent.click(screen.getByLabelText("page.webmail.search_all_folders"));
    await waitFor(() =>
      expect(
        (
          screen.getByLabelText(
            "page.webmail.search_all_folders",
          ) as HTMLInputElement
        ).checked,
      ).toBe(true),
    );
    await waitFor(() =>
      expect(
        (screen.getByLabelText("page.webmail.search_aria") as HTMLInputElement)
          .disabled,
      ).toBe(false),
    );
    fireEvent.change(screen.getByLabelText("page.webmail.search_aria"), {
      target: { value: q },
    });
    fireEvent.submit(screen.getByRole("search"));
  }
  const crossRows = [
    {
      uid: 1,
      uid_validity: 9,
      folder_enc: "b64.SU5CT1g",
      folder: "INBOX",
      subject: "Cross inbox",
      from: [],
      date: "2026-01-01",
      flags: ["\\Seen"],
      size: 1,
    },
    {
      uid: 1,
      uid_validity: 11,
      folder_enc: "b64.QXJjaGl2ZQ",
      folder: "Archive",
      subject: "Cross archive",
      from: [],
      date: "2026-01-02",
      flags: ["\\Seen"],
      size: 1,
    },
  ];

  it("removes old actionable detail while the next message is slow or fails", async () => {
    sessionStorageReady();
    const base = mockMailboxApi();
    let finish!: (value: unknown) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (/messages\/2(?:\?|$)/.test(String(url)))
          return new Promise((resolve) => {
            finish = resolve;
          });
        return base(url, init);
      }),
    );
    renderMailbox();
    await screen.findByText("Hello inbox");
    fireEvent.click(screen.getByText("Hello inbox"));
    await screen.findByText("body");
    fireEvent.click(screen.getByText("Second mail"));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    expect(screen.queryByText("body")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "page.webmail.compose_reply" }),
    ).toBeNull();
    await act(async () =>
      finish(response({ error: { code: "IMAP_UNAVAILABLE" } }, 502)),
    );
    expect(screen.queryByText("body")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "page.webmail.compose_reply" }),
    ).toBeNull();
  });

  it("retains the current query when sorting or grouping", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi();
    renderMailbox();
    await screen.findByText("Hello inbox");
    fireEvent.change(screen.getByLabelText("page.webmail.search_aria"), {
      target: { value: "needle" },
    });
    fireEvent.submit(screen.getByRole("search"));
    await waitFor(() =>
      expect(
        (screen.getByLabelText("page.webmail.search_aria") as HTMLInputElement)
          .disabled,
      ).toBe(false),
    );
    fireEvent.change(screen.getByLabelText("page.webmail.sort_by"), {
      target: { value: "subject" },
    });
    await waitFor(() =>
      expect(
        String(
          fetcher.mock.calls
            .filter((call) => String(call[0]).includes("/messages?"))
            .at(-1)?.[0],
        ),
      ).toContain("sort=subject"),
    );
    expect(
      (screen.getByLabelText("page.webmail.search_aria") as HTMLInputElement)
        .value,
    ).toBe("needle");
    expect(
      String(
        fetcher.mock.calls
          .filter((call) => String(call[0]).includes("/messages?"))
          .at(-1)?.[0],
      ),
    ).toContain("q=needle");
  });

  it("opens equal UIDs independently and routes quick flags to their actual source", async () => {
    sessionStorageReady();
    const base = mockMailboxApi();
    const fetcher = vi.fn((url: string, init?: RequestInit) => {
      const u = String(url);
      if (u.startsWith("/api/v1/mailbox/search?"))
        return Promise.resolve(response({ messages: crossRows, total: 2 }));
      if (/messages\/1(?:\?|$)/.test(u)) {
        const source = crossRows.find((row) =>
          u.includes(`/folders/${row.folder_enc}/`),
        )!;
        return Promise.resolve(
          response({
            ...source,
            text: `${source.subject} body`,
            attachments: [],
          }),
        );
      }
      return base(url, init);
    });
    vi.stubGlobal("fetch", fetcher);
    renderMailbox();
    await screen.findByText("Hello inbox");
    await searchAll();
    await screen.findByText("Cross archive");
    fireEvent.click(screen.getByText("Cross inbox"));
    await screen.findByText("Cross inbox body");
    fireEvent.click(screen.getByText("Cross archive"));
    await screen.findByText("Cross archive body");
    expect(screen.queryByText("Cross inbox body")).toBeNull();
    const row = screen
      .getAllByText("Cross archive")
      .find((node) => node.closest("li"))!
      .closest("li")!;
    fireEvent.click(
      within(row).getByRole("button", { name: "page.webmail.mark_unread" }),
    );
    await waitFor(() => {
      const call = fetcher.mock.calls.find((item) =>
        String(item[0]).includes("/folders/b64.QXJjaGl2ZQ/messages/flags"),
      );
      expect(call).toBeTruthy();
      expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({
        uids: [1],
        uid_validity: 11,
      });
    });
  });

  it("keeps failed cross-folder items selected and never retries already completed items", async () => {
    sessionStorageReady();
    const base = mockMailboxApi();
    const fetcher = vi.fn((url: string, init?: RequestInit) => {
      const u = String(url);
      if (u.startsWith("/api/v1/mailbox/search?"))
        return Promise.resolve(response({ messages: crossRows, total: 2 }));
      if (u.endsWith("/messages/trash"))
        return Promise.resolve(
          response({
            results: [
              {
                uid: 1,
                status: u.includes("b64.SU5CT1g") ? "success" : "failed",
              },
            ],
          }),
        );
      return base(url, init);
    });
    vi.stubGlobal("fetch", fetcher);
    renderMailbox();
    await screen.findByText("Hello inbox");
    await searchAll();
    await screen.findByText("Cross archive");
    for (const title of ["Cross inbox", "Cross archive"]) {
      fireEvent.click(
        within(screen.getByText(title).closest("li")!).getByRole("checkbox"),
      );
    }
    const deleteSelection = async () => {
      fireEvent.click(
        within(screen.getByRole("toolbar")).getByRole("button", {
          name: "page.webmail.delete",
        }),
      );
      const dialog = await screen.findByRole("dialog");
      fireEvent.click(
        within(dialog).getByRole("button", { name: "action.confirm" }),
      );
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    };
    await deleteSelection();
    expect(screen.queryByText("Cross inbox")).toBeNull();
    expect(
      within(screen.getByText("Cross archive").closest("li")!)
        .getByRole("checkbox")
        .getAttribute("type"),
    ).toBe("checkbox");
    await deleteSelection();
    const changes = fetcher.mock.calls.filter((call) =>
      String(call[0]).endsWith("/messages/trash"),
    );
    expect(
      changes.filter((call) => String(call[0]).includes("b64.SU5CT1g")),
    ).toHaveLength(1);
    expect(
      changes.filter((call) => String(call[0]).includes("b64.QXJjaGl2ZQ")),
    ).toHaveLength(2);
  });

  it("ignores delayed cross-folder search after switching folders", async () => {
    sessionStorageReady();
    const base = mockMailboxApi();
    let finish!: (value: unknown) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) =>
        String(url).startsWith("/api/v1/mailbox/search?")
          ? new Promise((resolve) => {
              finish = resolve;
            })
          : base(url, init),
      ),
    );
    renderMailbox();
    await screen.findByText("Hello inbox");
    await searchAll("delayed");
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    fireEvent.click(screen.getByRole("button", { name: "Archive" }));
    await waitFor(() =>
      expect(
        (screen.getByLabelText("page.webmail.search_aria") as HTMLInputElement)
          .value,
      ).toBe(""),
    );
    await act(async () => finish(response({ messages: crossRows, total: 2 })));
    expect(screen.queryByText("Cross archive")).toBeNull();
    expect(screen.getByText("Hello inbox")).toBeTruthy();
  });
});

it("preserves a visibly edited inline reply when autosave fails during navigation", async () => {
  sessionStorageReady();
  const base = mockMailboxApi();
  const saves: unknown[] = [];
  const fetcher = vi.fn((url: string, init?: RequestInit) => {
    if (String(url) === "/api/v1/mailbox/drafts" && init?.method === "PUT") {
      saves.push(JSON.parse(String(init.body)));
      return Promise.resolve({
        ok: false,
        status: 503,
        json: async () => ({ error: { code: "DB_ERROR" } }),
      });
    }
    return base(url, init);
  });
  vi.stubGlobal("fetch", fetcher);
  render(
    <MemoryRouter>
      <MailboxInboxPage />
    </MemoryRouter>,
  );
  await screen.findByText("Hello inbox");
  fireEvent.click(screen.getByText("Hello inbox"));
  await screen.findByText("body");
  fireEvent.click(
    screen
      .getAllByRole("button", {
        name: "page.webmail.compose_reply",
      })
      .at(-1)!,
  );
  const editor = await screen.findByRole("textbox", {
    name: "page.webmail.compose_rich_text",
  });
  editor.innerHTML = "<p>Unsaved visible reply</p>";
  fireEvent.input(editor);
  await waitFor(() =>
    expect(
      (
        screen.getByLabelText(
          "page.webmail.compose_body",
        ) as HTMLTextAreaElement
      ).value,
    ).toBe("Unsaved visible reply"),
  );
  fireEvent.click(screen.getByText("Second mail"));
  await waitFor(() => expect(saves.length).toBeGreaterThan(0));
  expect(
    fetcher.mock.calls.some((call) =>
      /messages\/2(?:\?|$)/.test(String(call[0])),
    ),
  ).toBe(false);
  expect(
    screen.getByRole("textbox", { name: "page.webmail.compose_rich_text" })
      .textContent,
  ).toBe("Unsaved visible reply");
  fireEvent.click(screen.getByLabelText("page.webmail.search_all_folders"));
  await act(async () => {});
  expect(
    (
      screen.getByLabelText(
        "page.webmail.search_all_folders",
      ) as HTMLInputElement
    ).checked,
  ).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Archive" }));
  await act(async () => {});
  expect(
    fetcher.mock.calls.some((call) =>
      String(call[0]).includes("/folders/b64.QXJjaGl2ZQ/messages"),
    ),
  ).toBe(false);
  const navigation = screen.getByRole("navigation", {
    name: "page.webmail.mobile_navigation",
  });
  fireEvent.click(
    within(navigation).getByRole("button", {
      name: "page.webmail.contacts_title",
    }),
  );
  await act(async () => {});
  expect(
    screen.queryByRole("heading", { name: "page.webmail.contacts_title" }),
  ).toBeNull();
  expect(
    screen.getByRole("textbox", { name: "page.webmail.compose_rich_text" })
      .textContent,
  ).toBe("Unsaved visible reply");
});

describe("mailbox workspace and search enhancements (WP02 & WP03)", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
    vi.restoreAllMocks();
  });

  function renderMailbox() {
    return render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );
  }

  it("supports one-click move to Trash with undo notification", async () => {
    sessionStorageReady();
    const fetcher = mockMailboxApi();
    renderMailbox();

    await screen.findByText("Hello inbox");
    const row = screen.getByText("Hello inbox").closest("li")!;
    const deleteBtn = within(row).getByRole("button", {
      name: "page.webmail.delete",
    });

    fireEvent.click(deleteBtn);

    // Confirm dialog should NOT appear for single message one-click delete
    expect(screen.queryByRole("dialog")).toBeNull();

    // Undo notification should appear
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "action.undo" })).toBeTruthy();
    });

    // Verify move to trash API call was made
    const trashCalls = fetcher.mock.calls.filter((c) =>
      String(c[0]).endsWith("/messages/trash"),
    );
    expect(trashCalls.length).toBeGreaterThan(0);

    // Clicking Undo should trigger restore
    fireEvent.click(screen.getByRole("button", { name: "action.undo" }));
    await waitFor(() => {
      const restoreCalls = fetcher.mock.calls.filter((c) =>
        String(c[0]).includes("/messages/restore"),
      );
      expect(restoreCalls.length).toBeGreaterThan(0);
      const body = JSON.parse(String(restoreCalls[0][1]?.body));
      expect(body.uids).toEqual([1]);
    });
  });

  it("handles read timer delay and cancellation on blur or manual cancel", async () => {
    sessionStorageReady();
    localStorage.setItem("urmail.webmail.readTimerMode", JSON.stringify("5s"));
    const fetcher = mockMailboxApi();
    renderMailbox();

    await screen.findByText("Hello inbox");
    // Message 1 ("Hello inbox") has empty flags (unseen)
    fireEvent.click(screen.getByText("Hello inbox"));
    await screen.findByText("body");

    // Marking read indicator with Cancel button should be displayed
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "action.cancel" }),
      ).toBeTruthy();
    });

    // Clicking Cancel cancels the timer
    fireEvent.click(screen.getByRole("button", { name: "action.cancel" }));
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "action.cancel" })).toBeNull();
    });

    // Verify flagMessages with \Seen was not called for message 2
    const flagCalls = fetcher.mock.calls.filter(
      (c) =>
        String(c[0]).includes("/messages/flags") &&
        String(c[1]?.body).includes("\\Seen"),
    );
    expect(flagCalls).toHaveLength(0);
  });

  it("displays folder unread badge and updates with loaded message status", async () => {
    sessionStorageReady();
    mockMailboxApi();
    renderMailbox();

    await screen.findByText("Hello inbox");
    // Inbox has 1 unseen message (Second mail)
    await waitFor(() => {
      const badge = screen.getByTestId("unread-badge-INBOX");
      expect(badge).toBeTruthy();
      expect(badge.textContent).toBe("1");
    });
  });
});

