import { describe, expect, it } from "vitest";
import {
  decorateThreads,
  groupThreads,
  normalizeThreadSubject,
} from "./mailbox-threading";
import type { MessageSummary } from "./mailbox-api";

function msg(
  overrides: Partial<MessageSummary> & { uid: number },
): MessageSummary {
  return {
    subject: "Hello",
    from: [{ email: "a@example.test" }],
    date: "2026-01-01T00:00:00Z",
    flags: [],
    size: 1,
    ...overrides,
  };
}

describe("normalizeThreadSubject", () => {
  it("strips reply and forward markers including CJK", () => {
    expect(normalizeThreadSubject("Re: Re: 计划")).toBe("计划");
    expect(normalizeThreadSubject("Fwd: 计划")).toBe("计划");
    expect(normalizeThreadSubject("转发：计划")).toBe("计划");
    expect(normalizeThreadSubject("hello")).toBe("hello");
  });
});

describe("groupThreads", () => {
  it("links replies via in_reply_to chains", () => {
    const messages = [
      msg({ uid: 1, message_id: "<a@x>" }),
      msg({ uid: 2, in_reply_to: "<a@x>" }),
      msg({ uid: 3, subject: "Other" }),
    ];
    const groups = groupThreads(messages);
    expect(groups.get(1)?.messages.map((m) => m.uid)).toEqual([1, 2]);
    expect(groups.get(3)?.messages.map((m) => m.uid)).toEqual([3]);
  });

  it("falls back to normalized subject and ignores empty subjects", () => {
    const messages = [
      msg({ uid: 7, subject: "Re: 计划" }),
      msg({ uid: 5, subject: "计划" }),
      msg({ uid: 8, subject: "" }),
    ];
    const groups = groupThreads(messages);
    expect(groups.get(5)?.messages.map((m) => m.uid)).toEqual([5, 7]);
    expect(groups.get(8)?.messages).toHaveLength(1);
  });
});

describe("decorateThreads", () => {
  it("prefers server-side thread_id counts when present", () => {
    const out = decorateThreads([
      msg({ uid: 2, thread_id: 1, thread_count: 9 }),
      msg({ uid: 3, thread_id: 1 }),
    ]);
    expect(out.map((o) => o.threadCount)).toEqual([2, 2]);
  });

  it("groups client-side when no thread_id present", () => {
    const out = decorateThreads([
      msg({ uid: 1, subject: "Hi", message_id: "<h@x>" }),
      msg({ uid: 2, subject: "Re: Hi", in_reply_to: "<h@x>" }),
      msg({ uid: 3, subject: "Solo" }),
    ]);
    expect(out.map((o) => o.threadCount)).toEqual([2, 2, 1]);
  });
});
