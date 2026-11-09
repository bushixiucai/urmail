import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Inbox } from "lucide-react";
import { WebmailEmptyState } from "./webmail-empty-state";
import { WebmailMessageList } from "./webmail-message-list";
import type { MessageSummary } from "@/features/webmail/mailbox-api";

afterEach(() => cleanup());

function msg(uid: number, isUnread = false): MessageSummary {
  return {
    uid,
    subject: `s${uid}`,
    from: [{ name: "a", email: "a@example.test" }],
    date: "2026-11-09T00:00:00Z",
    flags: isUnread ? [] : ["\\Seen"],
    size: 10,
  };
}

type ListProps = Parameters<typeof WebmailMessageList>[0];

function renderList(props: Partial<ListProps>) {
  return render(
    <WebmailMessageList
      folderName="Inbox"
      messages={[]}
      activeUid={null}
      onSelectMessage={vi.fn()}
      density="comfortable"
      sort="date"
      order="desc"
      onSortChange={vi.fn()}
      {...props}
    />,
  );
}

it("renders the empty-state structure with an optional action", () => {
  const onAction = vi.fn();
  render(
    <WebmailEmptyState
      testId="empty-x"
      icon={Inbox}
      title="标题"
      description="说明文字"
      actionLabel="动作"
      onAction={onAction}
    />,
  );
  expect(screen.getByText("标题")).toBeTruthy();
  expect(screen.getByText("说明文字")).toBeTruthy();
  fireEvent.click(screen.getByText("动作"));
  expect(onAction).toHaveBeenCalledOnce();
});

it("folder empty state shows the Inbox scene with a compose action", () => {
  const onCompose = vi.fn();
  renderList({ onCompose });
  expect(screen.getByTestId("empty-folder")).toBeTruthy();
  fireEvent.click(screen.getByText("webmail_next.empty_folder_action"));
  expect(onCompose).toHaveBeenCalledOnce();
});

it("search empty state shows the SearchX scene with a clear action", () => {
  const onClearSearch = vi.fn();
  renderList({ isSearchView: true, onClearSearch });
  expect(screen.getByTestId("empty-search")).toBeTruthy();
  fireEvent.click(screen.getByText("webmail_next.empty_search_action"));
  expect(onClearSearch).toHaveBeenCalledOnce();
});

it("all-caught-up banner appears when every message is read", () => {
  renderList({ messages: [msg(1), msg(2)] });
  expect(screen.getByTestId("list-all-caught-up")).toBeTruthy();
});

it("all-caught-up banner stays hidden while an unread message exists", () => {
  renderList({ messages: [msg(1), msg(2, true)] });
  expect(screen.queryByTestId("list-all-caught-up")).toBeNull();
});
