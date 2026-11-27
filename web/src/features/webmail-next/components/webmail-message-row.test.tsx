import { describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
import type { WebmailItem } from "../types";
import {
  WebmailMessageRow,
  webmailMessageRowPropsAreEqual,
} from "./webmail-message-row";
import { render, screen } from "@testing-library/react";

const item: WebmailItem = {
  uid: 7,
  subject: "Hello",
  sender: "Ada",
  senderEmail: "ada@example.com",
  date: "2026-11-06T00:00:00Z",
  snippet: "preview",
  isUnread: true,
  isStarred: false,
  hasAttachments: false,
  size: 10,
};

const handlers = {
  onSelect: () => {},
  onToggleCheck: () => {},
  onToggleStar: () => {},
};

describe("webmailMessageRowPropsAreEqual", () => {
  const base = {
    item,
    isSelected: false,
    isActive: false,
    isCursor: false,
    density: "comfortable" as const,
    ...handlers,
  };

  it("ignores callback identity when the visible fields match", () => {
    expect(
      webmailMessageRowPropsAreEqual(base, {
        ...base,
        onSelect: () => {},
        onToggleCheck: () => {},
        onToggleStar: () => {},
      }),
    ).toBe(true);
  });

  it("rerenders when uid, flags, selection, or snippet change", () => {
    expect(
      webmailMessageRowPropsAreEqual(base, {
        ...base,
        item: { ...item, uid: 8 },
      }),
    ).toBe(false);
    expect(
      webmailMessageRowPropsAreEqual(base, {
        ...base,
        item: { ...item, isUnread: false },
      }),
    ).toBe(false);
    expect(
      webmailMessageRowPropsAreEqual(base, {
        ...base,
        item: { ...item, snippet: "other" },
      }),
    ).toBe(false);
    expect(
      webmailMessageRowPropsAreEqual(base, { ...base, isSelected: true }),
    ).toBe(false);
    expect(
      webmailMessageRowPropsAreEqual(base, { ...base, isActive: true }),
    ).toBe(false);
  });

  it("still renders the snippet inside the row", () => {
    render(<WebmailMessageRow {...base} />);
    expect(screen.getByTestId("mail-snippet-7").textContent).toBe("preview");
  });

  it("keeps timestamp visible with title and renders quick action buttons without hiding date (DEF-6)", () => {
    const onDelete = vi.fn();
    const onToggleRead = vi.fn();
    render(
      <WebmailMessageRow
        {...base}
        onDelete={onDelete}
        onToggleRead={onToggleRead}
      />,
    );
    const dateSpan = screen.getByTitle(/2026/);
    expect(dateSpan).toBeTruthy();
    expect(dateSpan.className).not.toContain("group-hover:hidden");

    expect(screen.getByLabelText("webmail_next.mark_read")).toBeTruthy();
    expect(screen.getByLabelText("webmail_next.delete")).toBeTruthy();
  });
});

describe("#119 unverified hint", () => {
  const baseItem = {
    uid: 900,
    subject: "bounce candidate",
    sender: "Mailer",
    senderEmail: "md@x",
    date: "2026-11-06T09:00:00Z",
    isUnread: false,
    isStarred: false,
    hasAttachments: false,
    size: 1,
    snippet: "",
  };
  const noop = () => {};

  it("renders the unverified hint without a thread pill", () => {
    render(
      <WebmailMessageRow
        item={{ ...baseItem, threadVerification: "unverified" } as never}
        density="comfortable"
        isSelected={false}
        isActive={false}
        onSelect={noop}
        onToggleCheck={noop}
        onToggleStar={noop}
        onToggleThread={noop}
      />,
    );
    expect(screen.getByTestId("thread-unverified-900")).toBeTruthy();
    expect(screen.queryByTestId(/^thread-toggle-/)).toBeNull();
  });

  it("omits the hint for normal rows", () => {
    render(
      <WebmailMessageRow
        item={baseItem as never}
        density="comfortable"
        isSelected={false}
        isActive={false}
        onSelect={noop}
        onToggleCheck={noop}
        onToggleStar={noop}
        onToggleThread={noop}
      />,
    );
    expect(screen.queryByTestId("thread-unverified-900")).toBeNull();
  });
});

describe("Q1/Q2 thread decisions (interview 2026-11-26)", () => {
  const noop = () => {};
  const rowItem = (over: Partial<WebmailItem>): WebmailItem => ({
    ...item,
    ...over,
  });
  const renderRow = (over: Partial<WebmailItem>) =>
    render(
      <WebmailMessageRow
        item={rowItem(over)}
        density="comfortable"
        isSelected={false}
        isActive={false}
        onSelect={noop}
        onToggleCheck={noop}
        onToggleStar={noop}
        onToggleThread={noop}
      />,
    );

  it("renders the weakened reply prefix on system-sender child rows", () => {
    renderRow({
      isThreadChild: true,
      sender: "Mail Delivery System",
      senderEmail: "mailer-daemon@mail.example.test",
    });
    const prefix = screen.getByText("webmail_next.reply");
    expect(prefix.className).toContain("text-[11px]");
    expect(prefix.className).toContain("var(--vellum-ink-muted)");
    expect(prefix.className).not.toContain("var(--vellum-ink-secondary)");
  });

  it("keeps the regular reply prefix on human child rows", () => {
    renderRow({
      isThreadChild: true,
      sender: "Bob",
      senderEmail: "bob@example.com",
    });
    const prefix = screen.getByText("webmail_next.reply");
    expect(prefix.className).toContain("text-xs");
    expect(prefix.className).toContain("var(--vellum-ink-secondary)");
    expect(prefix.className).not.toContain("text-[11px]");
  });

  it("switches the pill to the weakened related-count wording for weak threads", () => {
    renderRow({ threadId: 12, threadCount: 43, weakAggregation: true });
    const pill = screen.getByTestId("thread-toggle-12");
    expect(pill.textContent).toContain("webmail_next.thread_related_count");
    expect(pill.getAttribute("title")).toBe(
      "webmail_next.thread_related_count",
    );
  });

  it("keeps the bare count and normal title for non-weak threads", () => {
    renderRow({ threadId: 12, threadCount: 3 });
    const pill = screen.getByTestId("thread-toggle-12");
    expect(pill.textContent).toContain("3");
    expect(pill.getAttribute("title")).toBe(
      "webmail_next.thread_messages_count",
    );
  });
});
