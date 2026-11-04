import { describe, expect, it } from "vitest";
import {
  decorateThreads,
  groupThreads,
  normalizeThreadSubject,
  parseMessageIds,
} from "./mailbox-threading";
import type { MessageSummary } from "./mailbox-api";

function msg(
  overrides: Partial<MessageSummary> & {
    uid: number;
    references?: string | string[];
  },
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

describe("parseMessageIds", () => {
  it("extracts and strips angle brackets from message ID strings", () => {
    expect(parseMessageIds("<a@example.com> <b@example.com>")).toEqual([
      "a@example.com",
      "b@example.com",
    ]);
    expect(parseMessageIds(["<c@test>", "d@test"])).toEqual([
      "c@test",
      "d@test",
    ]);
    expect(parseMessageIds("")).toEqual([]);
  });
});

describe("groupThreads (WP02 / I04: References & In-Reply-To without subject-only merging)", () => {
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

  it("links messages using References header chains", () => {
    const messages = [
      msg({ uid: 10, message_id: "<root@domain>" }),
      msg({
        uid: 11,
        message_id: "<mid1@domain>",
        references: "<root@domain>",
      }),
      msg({
        uid: 12,
        message_id: "<mid2@domain>",
        references: "<root@domain> <mid1@domain>",
      }),
    ];
    const groups = groupThreads(messages);
    expect(groups.get(10)?.messages.map((m) => m.uid)).toEqual([10, 11, 12]);
  });

  it("links siblings sharing the same unseen parent reference", () => {
    const messages = [
      msg({ uid: 21, references: "<missing-parent@domain>" }),
      msg({ uid: 22, references: "<missing-parent@domain>" }),
    ];
    const groups = groupThreads(messages);
    expect(groups.get(21)?.messages.map((m) => m.uid)).toEqual([21, 22]);
  });

  it("NEVER merges purely by subject when no in_reply_to or references exist", () => {
    const messages = [
      msg({ uid: 5, subject: "计划" }),
      msg({ uid: 7, subject: "Re: 计划" }),
      msg({ uid: 8, subject: "计划" }),
    ];
    const groups = groupThreads(messages);
    // Each remains its own thread root
    expect(groups.get(5)?.messages.map((m) => m.uid)).toEqual([5]);
    expect(groups.get(7)?.messages.map((m) => m.uid)).toEqual([7]);
    expect(groups.get(8)?.messages.map((m) => m.uid)).toEqual([8]);
  });
});

describe("decorateThreads (Accurate conversation count not bounded by current page)", () => {
  it("preserves accurate conversation count when server supplies thread_count greater than loaded page", () => {
    const out = decorateThreads([
      msg({ uid: 2, thread_id: 1, thread_count: 9 }),
      msg({ uid: 3, thread_id: 1, thread_count: 9 }),
    ]);
    // The server thread_count (9) is not capped to 2 (loaded messages on page)
    expect(out.map((o) => o.threadCount)).toEqual([9, 9]);
  });

  it("calculates accurate count when thread_count is not provided by server", () => {
    const out = decorateThreads([
      msg({ uid: 1, subject: "Hi", message_id: "<h@x>" }),
      msg({ uid: 2, subject: "Re: Hi", in_reply_to: "<h@x>" }),
      msg({ uid: 3, subject: "Solo" }),
    ]);
    expect(out.map((o) => o.threadCount)).toEqual([2, 2, 1]);
  });
});
