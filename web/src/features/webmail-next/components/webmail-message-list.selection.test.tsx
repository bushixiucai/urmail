import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { WebmailMessageList } from "./webmail-message-list";
import type { MessageSummary } from "@/features/webmail/mailbox-api";

afterEach(() => cleanup());

function msg(uid: number): MessageSummary {
  return {
    uid,
    subject: `s${uid}`,
    from: [{ name: "a", email: "a@example.test" }],
    date: "2026-11-09T00:00:00Z",
    flags: [],
    size: 10,
  };
}

function renderList(onSelectionCountChange: (count: number) => void) {
  return render(
    <WebmailMessageList
      folderName="Inbox"
      messages={[msg(1), msg(2)]}
      activeUid={null}
      onSelectMessage={vi.fn()}
      density="comfortable"
      sort="date"
      order="desc"
      onSortChange={vi.fn()}
      onSelectionCountChange={onSelectionCountChange}
    />,
  );
}

it("reports selection count changes for the mobile FAB yield", () => {
  const onSelectionCountChange = vi.fn();
  renderList(onSelectionCountChange);
  // 初始 0
  expect(onSelectionCountChange).toHaveBeenLastCalledWith(0);
  // 全选 → 上报 2
  fireEvent.click(screen.getByTestId("select-all-button"));
  expect(onSelectionCountChange).toHaveBeenLastCalledWith(2);
  // 批量条激活
  expect(screen.getByTestId("webmail-bulk-bar")).toBeTruthy();
  // 再点全选取消 → 上报 0
  fireEvent.click(screen.getByTestId("select-all-button"));
  expect(onSelectionCountChange).toHaveBeenLastCalledWith(0);
});
