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

describe("§11-1 subject bar", () => {
  it("shows the expanded message subject with server total badge", () => {
    renderStack({ totalOverride: 761, unreadCount: 3 });
    const bar = screen.getByTestId("thread-subject-bar");
    expect(bar).toBeTruthy();
    expect(bar.textContent).toContain("Re: Project Status Update");
    expect(screen.getByTestId("thread-total-badge").textContent).toBe("761");
    const unread = screen.getByTestId("thread-unread-badge");
    expect(unread.textContent).toBe("3");
    expect(unread.getAttribute("aria-label")).toBe(
      "webmail_next.thread_unread_badge",
    );
  });

  it("hides the unread badge when nothing is unread", () => {
    renderStack({ unreadCount: 0 });
    expect(screen.queryByTestId("thread-unread-badge")).toBeNull();
  });

  it("degrades to a fallback subject when none is available", () => {
    renderStack({
      members: members.map((m) => ({ ...m, subject: "" })),
      expandedDetail: { subject: "" } as never,
    });
    expect(screen.getByTestId("thread-subject-bar").textContent).toContain(
      "page.webmail.no_subject",
    );
  });
});

describe("§11-5 single-message actions", () => {
  it("shows Star with aria-pressed and Delete only on the expanded card", () => {
    const onToggleStar = vi.fn();
    const onDelete = vi.fn();
    const unstarred = members.map((m) =>
      m.uid === 203
        ? { ...m, flags: m.flags.filter((f) => f !== "\\Flagged") }
        : m,
    );
    renderStack({ members: unstarred, onToggleStar, onDelete });
    expect(
      screen.getByTestId("thread-card-star-203").getAttribute("aria-pressed"),
    ).toBe("false");
    expect(screen.queryByTestId("thread-card-star-202")).toBeNull();
    expect(screen.queryByTestId("thread-card-delete-202")).toBeNull();
    expect(screen.getByTestId("thread-card-delete-203")).toBeTruthy();

    fireEvent.click(screen.getByTestId("thread-card-star-203"));
    expect(onToggleStar).toHaveBeenCalledWith(203);
    fireEvent.click(screen.getByTestId("thread-card-delete-203"));
    expect(onDelete).toHaveBeenCalledWith(203);
  });

  it("reflects the starred state via fill and aria-pressed", () => {
    const starred = members.map((m) =>
      m.uid === 203 ? { ...m, flags: [...m.flags, "\\Flagged"] } : m,
    );
    renderStack({ members: starred, onToggleStar: vi.fn() });
    const star = screen.getByTestId("thread-card-star-203");
    expect(star.getAttribute("aria-pressed")).toBe("true");
    expect(star.querySelector("svg")?.getAttribute("class")).toContain("fill-");
  });

  it("focuses the relayed card header when focusUid is set", () => {
    const { rerender } = renderStack({ expandedUid: 202, focusUid: null });
    rerender(
      <WebmailThreadStack
        members={members}
        expandedUid={202}
        expandedDetail={detail}
        bodySlot={<div data-testid="stack-body">BODY</div>}
        onExpand={vi.fn()}
        onReply={vi.fn()}
        onReplyAll={vi.fn()}
        onForward={vi.fn()}
        onMarkThreadRead={vi.fn()}
        onArchiveThread={vi.fn()}
        onDeleteThread={vi.fn()}
        focusUid={202}
      />,
    );
    expect(document.activeElement).toBe(
      screen.getByTestId("thread-card-header-202"),
    );
  });
});
