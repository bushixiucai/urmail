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
    expect(onSelect).toHaveBeenCalledWith(103);
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

    const sortBtn = screen.getByTestId("sort-toggle-button");
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
    expect(second).toHaveBeenCalledWith(103);
  });
});
