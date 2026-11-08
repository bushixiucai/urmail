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
