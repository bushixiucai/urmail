import { renderHook, act } from "@testing-library/react";
import { describe, it, expect } from "vitest";
import { useMessageThreading } from "./use-message-threading";
import type { MessageSummary } from "@/features/webmail/mailbox-api";

describe("useMessageThreading (DEF-2)", () => {
  it("uses server thread_count for thread badge and sender summary when count exceeds loaded messages", () => {
    // 6 loaded messages in a thread where server reports thread_count = 164
    const loadedMessages: MessageSummary[] = Array.from(
      { length: 6 },
      (_, i) => ({
        uid: 100 + i,
        subject: "Large Thread Subject",
        from: [
          { name: `Sender ${i % 2}`, email: `sender${i % 2}@example.com` },
        ],
        date: new Date(2026, 10, 8, 10, i).toISOString(),
        flags: [],
        size: 1024,
        thread_id: 42,
        thread_count: 164,
      }),
    );

    const { result } = renderHook(() => useMessageThreading(loadedMessages));

    expect(result.current.items).toHaveLength(1);
    const parentItem = result.current.items[0];
    expect(parentItem.threadCount).toBe(164);
    expect(parentItem.sender).toContain("(164)");

    // Expand the thread
    act(() => {
      result.current.toggleThreadExpand("legacy:42");
    });

    // Parent + 5 children = 6 visible items
    expect(result.current.items).toHaveLength(6);
    expect(result.current.items[0].threadCount).toBe(164);
    expect(result.current.items[0].isThreadExpanded).toBe(true);
    expect(result.current.items[0].childUids).toHaveLength(5);
  });
});

describe("#119 degraded contract: thread_verification", () => {
  it("plumbs thread_verification to the row item without fabricating group fields", () => {
    const { result } = renderHook(() =>
      useMessageThreading([
        {
          uid: 501,
          subject: "bounce candidate",
          from: [{ name: "Mailer", email: "md@x" }],
          date: "2026-11-06T09:00:00Z",
          flags: [],
          size: 1,
          thread_verification: "unverified",
        } as MessageSummary,
      ]),
    );
    const items = result.current.items;
    expect(items).toHaveLength(1);
    expect(items[0].threadVerification).toBe("unverified");
    expect(items[0].threadId).toBeUndefined();
    expect(items[0].threadCount).toBeUndefined();
  });
});

describe("weak_aggregation passthrough (Q2/Q4)", () => {
  it("plumbs the server weak stamp to the representative row item only", () => {
    const msg = (uid: number): MessageSummary => ({
      uid,
      subject: "giant thread",
      from: [{ name: "A", email: "a@x" }],
      date: new Date(2026, 10, 6, 10, uid % 60).toISOString(),
      flags: [],
      size: 1,
      thread_id: 77,
      thread_count: 43,
      weak_aggregation: true,
    });
    const { result } = renderHook(() =>
      useMessageThreading([msg(601), msg(602)]),
    );
    expect(result.current.items).toHaveLength(1);
    expect(result.current.items[0]!.weakAggregation).toBe(true);
  });
});
