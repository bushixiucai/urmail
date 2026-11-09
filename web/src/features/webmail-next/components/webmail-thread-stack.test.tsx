import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailThreadStack } from "./webmail-thread-stack";
import type { MessageSummary } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, opts?: Record<string, unknown>) => {
      if (k === "webmail_next.thread_loaded_count") {
        return `Loaded ${opts?.loaded} of ${opts?.total}`;
      }
      return k.replace("{{count}}", String(opts?.count ?? ""));
    },
  }),
}));

afterEach(() => {
  cleanup();
});

const members: MessageSummary[] = [
  {
    uid: 201,
    subject: "Project Status Update",
    from: [{ name: "Alice", email: "alice@example.com" }],
    date: "2026-11-06T10:00:00Z",
    flags: ["\\Seen"],
    size: 1024,
    thread_id: 10,
    thread_count: 3,
    preview: "Kicking off the project",
  },
  {
    uid: 202,
    subject: "Re: Project Status Update",
    from: [{ name: "Bob", email: "bob@example.com" }],
    date: "2026-11-06T11:00:00Z",
    flags: [],
    size: 2048,
    thread_id: 10,
    thread_count: 3,
    preview: "Sounds good, adding notes",
  },
  {
    uid: 203,
    subject: "Re: Project Status Update",
    from: [{ name: "Carol", email: "carol@example.com" }],
    date: "2026-11-06T12:00:00Z",
    flags: ["\\Flagged"],
    size: 512,
    thread_id: 10,
    thread_count: 3,
    preview: "Final deck attached",
  },
];

const detail = { ...members[2], text: "Final body copy" } as never;

function renderStack(overrides: Record<string, unknown> = {}) {
  return render(
    <WebmailThreadStack
      members={members}
      expandedUid={203}
      expandedDetail={detail}
      bodySlot={<div data-testid="stack-body">BODY</div>}
      onExpand={vi.fn()}
      onReply={vi.fn()}
      onReplyAll={vi.fn()}
      onForward={vi.fn()}
      onMarkThreadRead={vi.fn()}
      onArchiveThread={vi.fn()}
      onDeleteThread={vi.fn()}
      {...overrides}
    />,
  );
}

describe("WebmailThreadStack", () => {
  it("renders all member cards oldest→newest with exactly one expanded", () => {
    renderStack();
    const cards = screen.getAllByTestId(/^thread-card-\d+$/);
    expect(cards.map((c) => c.getAttribute("data-testid"))).toEqual([
      "thread-card-201",
      "thread-card-202",
      "thread-card-203",
    ]);
    expect(cards[2]!.getAttribute("data-expanded")).toBe("true");
    expect(cards[0]!.getAttribute("data-expanded")).toBe("false");
    // Only the expanded card shows a body region.
    expect(screen.getByTestId("stack-body")).toBeTruthy();
  });

  it("collapses the current card when another header is clicked", () => {
    const onExpand = vi.fn();
    renderStack({ onExpand });
    fireEvent.click(screen.getByTestId("thread-card-header-201"));
    expect(onExpand).toHaveBeenCalledWith(201);
  });

  it("disables prev at the oldest card and next at the newest", () => {
    const { rerender } = renderStack({ expandedUid: 201 });
    expect(
      (screen.getByTestId("thread-prev") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (screen.getByTestId("thread-next") as HTMLButtonElement).disabled,
    ).toBe(false);

    rerender(
      <WebmailThreadStack
        members={members}
        expandedUid={203}
        expandedDetail={detail}
        bodySlot={<div>body</div>}
        onExpand={vi.fn()}
        onReply={vi.fn()}
        onReplyAll={vi.fn()}
        onForward={vi.fn()}
        onMarkThreadRead={vi.fn()}
        onArchiveThread={vi.fn()}
        onDeleteThread={vi.fn()}
      />,
    );
    expect(
      (screen.getByTestId("thread-prev") as HTMLButtonElement).disabled,
    ).toBe(false);
    expect(
      (screen.getByTestId("thread-next") as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("navigates prev/next through the stack on click", () => {
    const onExpand = vi.fn();
    const { rerender } = renderStack({ expandedUid: 202, onExpand });
    fireEvent.click(screen.getByTestId("thread-prev"));
    expect(onExpand).toHaveBeenLastCalledWith(201);
    fireEvent.click(screen.getByTestId("thread-next"));
    expect(onExpand).toHaveBeenLastCalledWith(203);

    rerender(
      <WebmailThreadStack
        members={members}
        expandedUid={202}
        expandedDetail={null}
        bodySlot={<div>body</div>}
        onExpand={vi.fn()}
        onReply={vi.fn()}
        onReplyAll={vi.fn()}
        onForward={vi.fn()}
        onMarkThreadRead={vi.fn()}
        onArchiveThread={vi.fn()}
        onDeleteThread={vi.fn()}
      />,
    );
    // Reply actions only render when the expanded detail is loaded.
    expect(screen.queryByTestId("thread-card-reply")).toBeNull();
  });

  it("exposes thread-level actions and the member count", () => {
    renderStack();
    expect(screen.getByTestId("thread-mark-read")).toBeTruthy();
    expect(screen.getByTestId("thread-archive")).toBeTruthy();
    expect(screen.getByTestId("thread-delete")).toBeTruthy();
    expect(screen.getByTestId("thread-count-label").textContent).toBe(
      "Loaded 3 of 3",
    );
    expect(screen.getByTestId("thread-stack").getAttribute("aria-label")).toBe(
      "webmail_next.thread_stack_label",
    );
  });

  it("wires reply scope to the currently expanded card only", () => {
    const onReply = vi.fn();
    const expandedDetail = { ...members[1], text: "Bob body" } as never;
    renderStack({ expandedUid: 202, expandedDetail, onReply });
    fireEvent.click(screen.getByTestId("thread-card-reply"));
    expect(onReply).toHaveBeenCalledTimes(1);
    expect(onReply.mock.calls[0]![0].uid).toBe(202);
  });
});
