import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WebmailMessageList } from "./webmail-message-list";
import type { MessageSummary } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, vars?: { to?: number; total?: number }) =>
      vars ? `${key}:${vars.to}/${vars.total}` : key,
  }),
}));

afterEach(() => {
  cleanup();
});

function messages(count: number): MessageSummary[] {
  return Array.from({ length: count }, (_, index) => ({
    uid: index + 1,
    subject: `Subject ${index + 1}`,
    from: [{ name: "Ann", email: "ann@example.test" }],
    date: "2026-11-06T10:00:00Z",
    flags: ["\\Seen"],
    size: 100,
  }));
}

describe("WebmailMessageList virtual window", () => {
  it("keeps a 1000-message list under 100 row nodes and labels the loaded range", () => {
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={messages(1000)}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        totalCount={1000}
        viewportHeight={480}
      />,
    );
    const rows = document.querySelectorAll("[data-testid^='mail-row-']");
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(100);
    expect(screen.getByTestId("loaded-range").textContent).toContain(
      "webmail_next.loaded_range:1000/1000",
    );
  });

  it("asks for the next page when the scroller is within 240px of the end", () => {
    const onNearEnd = vi.fn();
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={messages(80)}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        viewportHeight={480}
        onNearEnd={onNearEnd}
      />,
    );
    const scroller = screen.getByTestId("message-rows-container");
    Object.defineProperty(scroller, "clientHeight", {
      configurable: true,
      value: 480,
    });
    Object.defineProperty(scroller, "scrollHeight", {
      configurable: true,
      value: 3200,
    });
    Object.defineProperty(scroller, "scrollTop", {
      configurable: true,
      value: 2500,
    });
    fireEvent.scroll(scroller);
    expect(onNearEnd).toHaveBeenCalled();
  });

  it("renders one skeleton row while the next page loads", () => {
    render(
      <WebmailMessageList
        folderName="Inbox"
        messages={messages(2)}
        activeUid={null}
        onSelectMessage={vi.fn()}
        density="compact"
        sort="date"
        order="desc"
        onSortChange={vi.fn()}
        isLoadingMore
      />,
    );
    expect(screen.getByTestId("list-page-skeleton")).toBeTruthy();
  });
});
