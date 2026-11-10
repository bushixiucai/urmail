import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailMessageList } from "./webmail-message-list";
import type { MessageSummary } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  cleanup();
});

const mockMessages: MessageSummary[] = [
  {
    uid: 101,
    subject: "Meeting Tomorrow",
    from: [{ name: "Alice", email: "alice@example.com" }],
    date: "2026-11-06T10:00:00Z",
    flags: ["\\Seen"],
    size: 1024,
    thread_id: 1,
    thread_count: 2,
  },
  {
    uid: 102,
    subject: "Re: Meeting Tomorrow",
    from: [{ name: "Bob", email: "bob@example.com" }],
    date: "2026-11-06T11:00:00Z",
    flags: [],
    size: 2048,
    thread_id: 1,
    thread_count: 2,
  },
  {
    uid: 103,
    subject: "Standalone Note",
    from: [{ name: "Charlie", email: "charlie@example.com" }],
    date: "2026-11-05T12:00:00Z",
    flags: ["\\Flagged"],
    size: 512,
  },
];

describe("WebmailMessageList", () => {
  it("renders messages and thread count pills", () => {
    const onSelect = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={onSelect}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );

    // Thread parent row should show thread count badge "2"
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.getByText("Standalone Note")).toBeTruthy();
  });

  it("expands a thread with indented children, reply label, and connector", () => {
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );

    // Before expand: only the representative (newest = uid 102) is shown.
    expect(screen.getByTestId("mail-row-102")).toBeTruthy();
    expect(screen.queryByTestId("mail-row-101")).toBeNull();

    fireEvent.click(screen.getByTestId("thread-toggle-1"));

    const child = screen.getByTestId("mail-row-101");
    expect(child.getAttribute("data-thread-child")).toBe("true");
    // Indent (pl-5 = 20px) and subtle background mark the hierarchy.
    expect(child.className).toContain("bg-[var(--vellum-surface-subtle)]");
    // Reply semantic label
    expect(child.textContent).toContain("webmail_next.reply");
    // Connector stubs are present and decorative.
    expect(child.querySelectorAll("[aria-hidden]").length).toBeGreaterThan(0);
  });

  it("handles Shift-click range multi-select", () => {
    const onSelect = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={onSelect}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );

    const firstCheck = screen.getByTestId("mail-check-102");
    const secondCheck = screen.getByTestId("mail-check-103");
    const firstRow = screen.getByTestId("mail-row-102");
    const secondRow = screen.getByTestId("mail-row-103");

    fireEvent.click(firstCheck);
    expect(firstRow.getAttribute("data-selected")).toBe("true");
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(secondCheck, { shiftKey: true });
    expect(firstRow.getAttribute("data-selected")).toBe("true");
    expect(secondRow.getAttribute("data-selected")).toBe("true");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("opens a message from the row without selecting it", () => {
    const onSelect = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={onSelect}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByTestId("mail-row-103"));
    expect(onSelect).toHaveBeenCalledWith(103, undefined);
    expect(
      screen.getByTestId("mail-row-103").getAttribute("data-selected"),
    ).toBe("false");
  });

  it("cycles sorting on sort button click", () => {
    const onSortChange = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={onSortChange}
      />,
    );

    // The visible label and tooltip are localised (no hardcoded "Date").
    expect(screen.getByText("webmail_next.sort_field_date")).toBeTruthy();
    const sortBtn = screen.getByTestId(
      "sort-toggle-button",
    ) as HTMLButtonElement;
    expect(sortBtn.title).toBe("webmail_next.sort_tooltip");

    fireEvent.click(sortBtn);
    expect(onSortChange).toHaveBeenCalledWith("date", "asc");
  });

  it("renders list_only density with the shared two-line structure", () => {
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="list_only"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );

    const firstRow = screen.getByTestId("mail-row-102");
    expect(firstRow.className).toContain("mail-row-item");
    expect(firstRow.getAttribute("data-density")).toBe("list_only");
    expect(firstRow.className).toContain("h-8");
    expect(screen.getByTestId("mail-snippet-102")).toBeTruthy();
    expect(screen.getByText("Re: Meeting Tomorrow")).toBeTruthy();
  });

  it("uses the latest select handler after a pagination rerender", () => {
    const first = vi.fn();
    const second = vi.fn();
    const view = render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={first}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        viewportHeight={640}
      />,
    );
    view.rerender(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages.map((message) => ({ ...message }))}
        activeUid={null}
        onSelectMessage={second}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        viewportHeight={640}
      />,
    );
    fireEvent.click(screen.getByTestId("mail-row-103"));
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledWith(103, undefined);
  });

  it("renders decoded folder name in list header when passed b64 encoded folderName", () => {
    const { unmount } = render(
      <WebmailMessageList
        folderName="b64.SU5CT1g"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    expect(screen.getByTestId("folder-name-header").textContent).toBe("INBOX");
    unmount();

    render(
      <WebmailMessageList
        folderName="b64.5pS25Lu2566x"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    expect(screen.getByTestId("folder-name-header").textContent).toBe("收件箱");
  });

  it("decodes folder names in cross-folder message rows and bulk move menu", () => {
    const messagesWithFolder = [
      {
        ...mockMessages[0],
        folder: "b64.6YeN6KaB6YKu5Lu2", // 重要邮件
      },
    ];

    render(
      <WebmailMessageList
        folderName="Search Results"
        messages={messagesWithFolder}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        moveFolders={[
          {
            name: "b64.UHJvamVjdCBBbHBoYQ", // Project Alpha
            name_enc: "b64.UHJvamVjdCBBbHBoYQ",
          },
        ]}
      />,
    );

    // Cross-folder badge should show decoded name
    expect(screen.getByTestId("mail-folder-101").textContent).toBe("重要邮件");

    // Select row and open bulk move menu
    fireEvent.click(screen.getByTestId("mail-check-101"));
    fireEvent.click(screen.getByTestId("bulk-move"));
    const bulkMenu = screen.getByTestId("bulk-move-menu");
    expect(bulkMenu.textContent).toContain("Project Alpha");
  });
});

describe("list header hotzones and no-wrap (B3-2/B3-3)", () => {
  it("gives select-all and refresh >=24px hit areas (p-1.5 + 14px icon)", () => {
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );
    const selectAll = screen.getByTestId("select-all-button");
    expect(selectAll.className).toContain("p-1.5");
    // Playwright e2e asserts real px; here we pin the class contract.
    expect(selectAll.querySelector("svg")?.getAttribute("class")).toContain(
      "w-3.5",
    );
  });

  it("keeps the loaded range on one line with an md short form", () => {
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    const range = screen.getByTestId("loaded-range");
    expect(range.className).toContain("truncate");
    expect(range.className).toContain("whitespace-nowrap");
    expect(range.textContent).toContain("webmail_next.loaded_range_short");
    expect(screen.getByTestId("sort-toggle-button").className).toContain(
      "whitespace-nowrap",
    );
  });
});
describe("thread pill routing (C1 D-D)", () => {
  it("opens the stack instead of expanding when members are unloaded", () => {
    const onSelectMessage = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={[mockMessages[1]]}
        activeUid={null}
        onSelectMessage={onSelectMessage}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    // uid 102 declares thread_count 2 but only itself is loaded: the pill
    // must not inline-expand (0-child dead end) — it opens the stack.
    fireEvent.click(screen.getByTestId("thread-toggle-1"));
    expect(onSelectMessage).toHaveBeenCalledWith(102, undefined);
    expect(screen.queryByTestId("mail-row-101")).toBeNull();
  });

  it("still inline-expands when every member is loaded", () => {
    const onSelectMessage = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={mockMessages}
        activeUid={null}
        onSelectMessage={onSelectMessage}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByTestId("thread-toggle-1"));
    expect(onSelectMessage).not.toHaveBeenCalled();
    expect(screen.getByTestId("mail-row-101")).toBeTruthy();
  });
});

describe("cross-folder same-uid list identity (QA/UIUX review)", () => {
  const dupRows = [
    {
      uid: 148,
      subject: "cross-box sample",
      from: [{ name: "Ann", email: "ann@x" }],
      date: "2026-11-06T10:00:00Z",
      flags: [],
      size: 1,
      folder: "INBOX",
      folder_enc: "b64.SU5CT1g",
    },
    {
      uid: 148,
      subject: "Re: cross-box sample",
      from: [{ name: "Me", email: "me@x" }],
      date: "2026-11-06T11:00:00Z",
      flags: [],
      size: 1,
      folder: "Sent",
      folder_enc: "b64.U2VudA",
    },
  ] as never;

  it("renders both same-uid rows with unique keys and passes each row's folderEnc", () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const onSelect = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        activeFolderEnc="b64.SU5CT1g"
        messages={dupRows}
        activeUid={null}
        onSelectMessage={onSelect}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    const rows = screen.getAllByTestId("mail-row-148");
    expect(rows).toHaveLength(2);
    fireEvent.click(rows[0]);
    expect(onSelect).toHaveBeenLastCalledWith(148, "b64.SU5CT1g");
    fireEvent.click(rows[1]);
    expect(onSelect).toHaveBeenLastCalledWith(148, "b64.U2VudA");
    const duplicateKeyWarnings = errorSpy.mock.calls.filter((args) =>
      String(args[0]).includes("same key"),
    );
    expect(duplicateKeyWarnings).toHaveLength(0);
    errorSpy.mockRestore();
  });

  it("does not let a foreign-box row join the folder-scoped selection", () => {
    const onSelectionCountChange = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        activeFolderEnc="b64.SU5CT1g"
        messages={dupRows}
        activeUid={null}
        onSelectMessage={vi.fn()}
        onSelectionCountChange={onSelectionCountChange}
        density="comfortable"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
      />,
    );
    const rows = screen.getAllByTestId("mail-row-148");
    const checkForeign = rows[1].querySelector(
      '[data-testid^="row-check"], input[type="checkbox"], button',
    );
    if (checkForeign) {
      fireEvent.click(checkForeign);
    }
    // Foreign row must not produce a selection event for INBOX bulk ops.
    expect(onSelectionCountChange).not.toHaveBeenCalledWith(1);
  });
});

describe("entry-matrix guards (QA/UIUX static review round 8)", () => {
  const rows = [
    {
      uid: 148,
      subject: "cross-box sample",
      from: [{ name: "Ann", email: "ann@x" }],
      date: "2026-11-06T10:00:00Z",
      flags: [],
      size: 1,
      folder: "INBOX",
      folder_enc: "b64.SU5CT1g",
    },
    {
      uid: 148,
      subject: "Re: cross-box sample",
      from: [{ name: "Me", email: "me@x" }],
      date: "2026-11-06T11:00:00Z",
      flags: [],
      size: 1,
      folder: "Sent",
      folder_enc: "b64.U2VudA",
    },
  ] as never;

  const baseProps = {
    folderName: "Inbox",
    activeFolderEnc: "b64.SU5CT1g",
    messages: rows,
    activeUid: null,
    density: "comfortable" as const,
    sort: "date" as const,
    order: "desc" as const,
    onSortChange: vi.fn(),
  };

  it("select-all only selects rows of the browsing folder", () => {
    const onSelectionCountChange = vi.fn();
    render(
      <WebmailMessageList
        {...baseProps}
        onSelectMessage={vi.fn()}
        onSelectionCountChange={onSelectionCountChange}
        onBulkArchive={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByTestId("select-all-button"));
    expect(onSelectionCountChange).toHaveBeenLastCalledWith(1);
  });

  it("shift-range never pulls foreign-box rows into the selection", () => {
    const onSelectionCountChange = vi.fn();
    render(
      <WebmailMessageList
        {...baseProps}
        onSelectMessage={vi.fn()}
        onSelectionCountChange={onSelectionCountChange}
        onBulkArchive={vi.fn()}
      />,
    );
    const checks = screen.getAllByTestId("mail-check-148");
    fireEvent.click(checks[0]);
    expect(onSelectionCountChange).toHaveBeenLastCalledWith(1);
    fireEvent.click(checks[1], { shiftKey: true });
    // Foreign row must not extend the selection beyond the browsing folder.
    expect(onSelectionCountChange).toHaveBeenLastCalledWith(1);
  });

  it("row quick actions are inert for foreign-box rows", () => {
    const onToggleStar = vi.fn();
    const onDeleteMessage = vi.fn();
    const onToggleRead = vi.fn();
    render(
      <WebmailMessageList
        {...baseProps}
        onSelectMessage={vi.fn()}
        onToggleStar={onToggleStar}
        onDeleteMessage={onDeleteMessage}
        onToggleRead={onToggleRead}
      />,
    );
    const sentRow = screen.getAllByTestId("mail-row-148")[1];
    const buttons = sentRow.querySelectorAll("button");
    buttons.forEach((b) => fireEvent.click(b));
    expect(onToggleStar).not.toHaveBeenCalled();
    expect(onDeleteMessage).not.toHaveBeenCalled();
    expect(onToggleRead).not.toHaveBeenCalled();
  });
});

describe("keyboard & mixed select-all (QA sequence ③④)", () => {
  const rows2 = [
    {
      uid: 148,
      subject: "inbox copy",
      from: [{ name: "A", email: "a@x" }],
      date: "2026-11-06T10:00:00Z",
      flags: [],
      size: 1,
      folder: "INBOX",
      folder_enc: "b64.SU5CT1g",
    },
    {
      uid: 148,
      subject: "sent copy",
      from: [{ name: "M", email: "m@x" }],
      date: "2026-11-06T11:00:00Z",
      flags: [],
      size: 1,
      folder: "Sent",
      folder_enc: "b64.U2VudA",
    },
  ] as never;

  const props = {
    folderName: "Inbox",
    activeFolderEnc: "b64.SU5CT1g",
    messages: rows2,
    activeUid: null,
    density: "comfortable" as const,
    sort: "date" as const,
    order: "desc" as const,
    onSortChange: vi.fn(),
  };

  it("Shift+F10 never opens the menu for an ambiguous same-uid row", () => {
    render(
      <WebmailMessageList
        {...props}
        onSelectMessage={vi.fn()}
        onDeleteMessage={vi.fn()}
        contextHandlers={undefined}
      />,
    );
    const rows = screen.getAllByTestId("mail-row-148");
    (rows[1] as HTMLElement).focus();
    fireEvent.keyDown(window, { key: "F10", shiftKey: true });
    // No row-context-menu testids should appear for the foreign/ambiguous row.
    expect(screen.queryAllByTestId(/^row-context-menu/)).toHaveLength(0);
  });

  it("select-all toggles against the selectable set only", () => {
    const onSelectionCountChange = vi.fn();
    render(
      <WebmailMessageList
        {...props}
        onSelectMessage={vi.fn()}
        onSelectionCountChange={onSelectionCountChange}
        onBulkArchive={vi.fn()}
      />,
    );
    const selectAll = screen.getByTestId("select-all-button");
    fireEvent.click(selectAll);
    expect(onSelectionCountChange).toHaveBeenLastCalledWith(1);
    fireEvent.click(selectAll);
    expect(onSelectionCountChange).toHaveBeenLastCalledWith(0);
  });

  it("single-box select-all still selects every row", () => {
    const single = [
      {
        uid: 148,
        subject: "inbox copy",
        from: [{ name: "A", email: "a@x" }],
        date: "2026-11-06T10:00:00Z",
        flags: [],
        size: 1,
      },
      {
        uid: 149,
        subject: "second",
        from: [{ name: "A", email: "a@x" }],
        date: "2026-11-06T09:00:00Z",
        flags: [],
        size: 1,
      },
    ] as never;
    const onSelectionCountChange = vi.fn();
    render(
      <WebmailMessageList
        {...props}
        activeFolderEnc={undefined}
        messages={single}
        onSelectMessage={vi.fn()}
        onSelectionCountChange={onSelectionCountChange}
        onBulkArchive={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByTestId("select-all-button"));
    expect(onSelectionCountChange).toHaveBeenLastCalledWith(2);
  });
});
