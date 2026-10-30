import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  MailboxMessageRow,
  type MailboxMessageRowProps,
} from "./mailbox-message-row";
import type { MessageSummary } from "./mailbox-api";

const mockT = vi.fn((k: string, o?: Record<string, unknown>) => {
  if (o?.count != null) return `${k}:${o.count}`;
  return k;
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: mockT,
    i18n: { language: "en", exists: () => true },
  }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function createMessage(
  overrides: Partial<MessageSummary> = {},
): MessageSummary {
  return {
    uid: 101,
    subject: "Project Update Q4",
    from: [{ name: "Alex Chen", email: "alex@example.test" }],
    date: "2026-10-28T09:30:00Z",
    flags: [],
    size: 2048,
    ...overrides,
  };
}

function renderRow(props: Partial<MailboxMessageRowProps> = {}) {
  const onSelect = vi.fn();
  const onToggleCheck = vi.fn();
  const onToggleFlag = vi.fn();
  const onToggleRead = vi.fn();
  const onDelete = vi.fn();
  const onThreadFilter = vi.fn();

  const fullProps: MailboxMessageRowProps = {
    message: createMessage(),
    isSelected: false,
    isChecked: false,
    threadCount: 1,
    onSelect,
    onToggleCheck,
    onToggleFlag,
    onToggleRead,
    onDelete,
    dateText: "10:30 AM",
    ...props,
  };

  const utils = render(
    <ul>
      <MailboxMessageRow {...fullProps} />
    </ul>,
  );

  return {
    ...utils,
    props: fullProps,
    onSelect,
    onToggleCheck,
    onToggleFlag,
    onToggleRead,
    onDelete,
    onThreadFilter,
  };
}

describe("MailboxMessageRow", () => {
  it("renders unread state with solid primary blue indicator dot and bold typography", () => {
    const { container } = renderRow({
      message: createMessage({ flags: [] }), // No \\Seen -> unread
    });

    const unreadDot = screen.getByTestId("unread-indicator");
    expect(unreadDot.getAttribute("data-unread")).toBe("true");
    expect(unreadDot.className).toContain("bg-primary");

    // Sender and Subject bold when unread
    const sender = screen.getByText("Alex Chen <alex@example.test>");
    expect(sender.className).toContain("font-semibold");

    const subject = screen.getByText("Project Update Q4");
    expect(subject.className).toContain("font-semibold");

    // Date is rendered
    expect(screen.getByText("10:30 AM")).toBeTruthy();

    // Check row container classes
    const row = container.querySelector("li");
    expect(row?.className).toContain("border-l-transparent");
  });

  it("renders read state with transparent indicator placeholder and muted typography", () => {
    renderRow({
      message: createMessage({ flags: ["\\Seen"] }),
    });

    const unreadDot = screen.getByTestId("unread-indicator");
    expect(unreadDot.getAttribute("data-unread")).toBe("false");
    expect(unreadDot.className).toContain("bg-transparent");

    const sender = screen.getByText("Alex Chen <alex@example.test>");
    expect(sender.className).toContain("font-normal");

    const subject = screen.getByText("Project Update Q4");
    expect(subject.className).toContain("text-muted-foreground");
  });

  it("applies highlight background and primary accent border when isSelected is true", () => {
    const { container } = renderRow({
      isSelected: true,
    });

    const row = container.querySelector("li");
    expect(row?.className).toContain("bg-muted");
    expect(row?.className).toContain("border-l-primary");
  });

  it("renders native checkbox, reflects isChecked, and handles click without triggering onSelect", () => {
    const { onToggleCheck, onSelect } = renderRow({
      isChecked: true,
      message: createMessage({ subject: "Budget Report" }),
    });

    const checkbox = screen.getByLabelText(
      "page.webmail.select_message_named",
    ) as HTMLInputElement;
    expect(checkbox).toBeTruthy();
    expect(checkbox.checked).toBe(true);

    // Click checkbox
    fireEvent.click(checkbox);
    expect(onToggleCheck).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("triggers onSelect when clicking the main message row", () => {
    const { onSelect } = renderRow();

    const subject = screen.getByText("Project Update Q4");
    fireEvent.click(subject);

    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("does not infer an attachment from a large message body", () => {
    renderRow({
      message: createMessage({ size: 65 * 1024, has_attachments: false }),
    });

    expect(screen.queryByTestId("attachment-icon")).toBeNull();
  });

  it("displays paperclip icon when has_attachments flag is present", () => {
    renderRow({
      message: createMessage({
        size: 1024,
        has_attachments: true,
      } as Partial<MessageSummary>),
    });

    expect(screen.getByTestId("attachment-icon")).toBeTruthy();
  });

  it("does not display paperclip icon when size is small and no attachments exist", () => {
    renderRow({
      message: createMessage({ size: 5 * 1024 }),
    });

    expect(screen.queryByTestId("attachment-icon")).toBeNull();
  });

  it("displays flagged indicator when message has \\Flagged flag", () => {
    renderRow({
      message: createMessage({ flags: ["\\Flagged"] }),
    });

    // Row header displays flag icon
    const flagIcons = screen.getAllByLabelText("page.webmail.flag");
    expect(flagIcons.length).toBeGreaterThanOrEqual(1);

    // Quick action shows unflag
    expect(screen.getByLabelText("page.webmail.unflag")).toBeTruthy();
  });

  it("handles hover actions: flag, mark read/unread, and delete with stopPropagation", () => {
    const { onToggleFlag, onToggleRead, onDelete, onSelect } = renderRow({
      message: createMessage({ flags: [] }), // unread, not flagged
    });

    // Flag button
    const flagBtn = screen.getByLabelText("page.webmail.flag");
    fireEvent.click(flagBtn);
    expect(onToggleFlag).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();

    // Mark as read button (since unread)
    const readBtn = screen.getByLabelText("page.webmail.mark_read");
    fireEvent.click(readBtn);
    expect(onToggleRead).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();

    // Delete button
    const deleteBtn = screen.getByLabelText("page.webmail.delete");
    fireEvent.click(deleteBtn);
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("shows mark as unread in hover actions when message is already read", () => {
    const { onToggleRead } = renderRow({
      message: createMessage({ flags: ["\\Seen"] }),
    });

    const unreadBtn = screen.getByLabelText("page.webmail.mark_unread");
    fireEvent.click(unreadBtn);
    expect(onToggleRead).toHaveBeenCalledTimes(1);
  });

  it("renders thread button when threadCount > 1 and onThreadFilter is supplied", () => {
    const onThreadFilter = vi.fn();
    const { onSelect } = renderRow({
      threadCount: 3,
      onThreadFilter,
      crossResults: false,
    });

    const threadBtn = screen.getByLabelText("page.webmail.thread_filter:3");
    expect(threadBtn).toBeTruthy();
    expect(threadBtn.textContent).toContain("3");

    fireEvent.click(threadBtn);
    expect(onThreadFilter).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("does not render thread button when threadCount is 1 or crossResults is true", () => {
    const onThreadFilter = vi.fn();

    // threadCount is 1
    const { rerender } = renderRow({
      threadCount: 1,
      onThreadFilter,
    });
    expect(screen.queryByLabelText(/page\.webmail\.thread_filter/)).toBeNull();

    // crossResults is true with threadCount > 1
    rerender(
      <ul>
        <MailboxMessageRow
          message={createMessage()}
          isSelected={false}
          isChecked={false}
          threadCount={4}
          onSelect={vi.fn()}
          onToggleCheck={vi.fn()}
          onToggleFlag={vi.fn()}
          onToggleRead={vi.fn()}
          onDelete={vi.fn()}
          onThreadFilter={onThreadFilter}
          crossResults={true}
          dateText="12:00 PM"
        />
      </ul>,
    );
    expect(screen.queryByLabelText(/page\.webmail\.thread_filter/)).toBeNull();
  });

  it("renders cross-folder badge when crossResults is true and folder is set", () => {
    renderRow({
      crossResults: true,
      message: createMessage({ folder: "Sent" }),
    });

    expect(screen.getByText("Sent")).toBeTruthy();
  });

  it("falls back gracefully when sender or subject is missing", () => {
    renderRow({
      message: createMessage({
        from: [],
        subject: "",
      }),
    });

    expect(screen.getByText("page.webmail.unknown_sender")).toBeTruthy();
    expect(screen.getByText("page.webmail.no_subject")).toBeTruthy();
  });
  it("renders compact density layout with smaller typography (WP03 / I02)", () => {
    renderRow({
      density: "compact",
      message: createMessage({ subject: "Compact Subject" }),
    });
    const subjectEl = screen.getByText("Compact Subject");
    expect(subjectEl.className).toContain("text-xs");
  });

  it("renders pure list density layout with single line tabular structure (WP03 / I02)", () => {
    const { container } = renderRow({
      density: "list",
      message: createMessage({ subject: "List Subject", size: 1048576 }),
      visibleColumns: { size: true },
    });
    const li = container.querySelector("li");
    expect(li?.className).toContain("h-10");
    expect(screen.getByText("List Subject")).toBeTruthy();
    expect(screen.getByText("1.0 MB")).toBeTruthy();
  });

  it("displays recipient instead of sender for Sent / Drafts messages (WP03 / I02)", () => {
    renderRow({
      isSentOrDrafts: true,
      message: {
        ...createMessage({
          from: [{ name: "Self", email: "me@example.test" }],
        }),
        to: [{ name: "Recipient User", email: "recipient@example.test" }],
      } as any,
    });

    expect(screen.getByText(/Recipient User/)).toBeTruthy();
    expect(screen.queryByText("Self")).toBeNull();
  });

  it("respects visibleColumns configuration to toggle columns (WP03 / I02)", () => {
    renderRow({
      density: "list",
      message: createMessage({ subject: "Hidden Subject", size: 2048 }),
      visibleColumns: { subject: false, size: true, fromTo: true, date: true, flags: true },
    });

    expect(screen.queryByText("Hidden Subject")).toBeNull();
    expect(screen.getByText("2 KB")).toBeTruthy();
  });
});

