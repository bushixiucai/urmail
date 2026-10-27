import type { MessageSummary } from "./mailbox-api";

/**
 * Client-side thread grouping over loaded summaries — the fallback path used
 * when the server did not stamp thread_id (no THREAD=REFERENCES on the IMAP
 * server, or the request was made without thread=1).
 *
 * Groups by in_reply_to -> message_id chains first, then by normalized
 * subject (RFC 5256-style base subject: reply/forward markers and CJK
 * equivalents stripped). Thread roots are the lowest UID in the group so ids
 * are stable across renders.
 */
export function normalizeThreadSubject(subject: string): string {
  let s = subject.trim();
  const prefixes = ["re:", "fw:", "fwd:", "回复:", "转发:", "转发："];
  for (;;) {
    const prev = s;
    const lower = s.toLowerCase();
    for (const prefix of prefixes) {
      if (lower.startsWith(prefix)) {
        s = s.slice(prefix.length).trim();
        break;
      }
    }
    if (s === prev) return s;
  }
}

export interface ThreadGroup {
  threadId: string;
  messages: MessageSummary[];
}

export function groupThreads(
  messages: MessageSummary[],
): Map<number, ThreadGroup> {
  const byUid = new Map<number, number>();
  const lowest: number[] = [];
  const parent = new Map<number, number>();
  const find = (u: number): number => {
    let root = u;
    while (parent.get(root) !== root) root = parent.get(root) as number;
    while (parent.get(u) !== u) {
      const next = parent.get(u) as number;
      parent.set(u, root);
      u = next;
    }
    return root;
  };
  const union = (a: number, b: number): void => {
    const ra = find(a);
    const rb = find(b);
    if (ra === rb) return;
    const [lo, hi] = ra < rb ? [ra, rb] : [rb, ra];
    parent.set(hi, lo);
  };

  const idToUid = new Map<string, number>();
  const sorted = [...messages].sort((a, b) => a.uid - b.uid);
  for (const m of sorted) {
    parent.set(m.uid, m.uid);
    const mid = (m.message_id ?? "").replace(/^<|>$/g, "");
    if (mid) idToUid.set(mid, m.uid);
  }
  for (const m of sorted) {
    const ref = (m.in_reply_to ?? "").replace(/^<|>$/g, "");
    if (ref && idToUid.has(ref)) union(m.uid, idToUid.get(ref) as number);
  }
  const bySubject = new Map<string, number>();
  for (const m of sorted) {
    const norm = normalizeThreadSubject(m.subject ?? "").toLowerCase();
    if (!norm) continue;
    const first = bySubject.get(norm);
    if (first != null) union(m.uid, first);
    else bySubject.set(norm, m.uid);
  }
  for (const m of sorted) {
    const root = find(m.uid);
    byUid.set(m.uid, root);
    lowest[root] = (lowest[root] ?? 0) + 1;
  }

  const groups = new Map<number, ThreadGroup>();
  for (const m of sorted) {
    const root = byUid.get(m.uid) as number;
    let group = groups.get(root);
    if (!group) {
      group = { threadId: String(root), messages: [] };
      groups.set(root, group);
    }
    group.messages.push(m);
  }
  return groups;
}

/**
 * UI grouping used by the inbox list: messages already carrying server-side
 * thread_id are grouped by it; otherwise groupThreads runs over the loaded
 * page. Returns (message, threadCount) pairs in the original order.
 */
export interface DecoratedThread {
  message: MessageSummary;
  threadCount: number;
  /** Stable key for the message's thread (root uid or server thread_id). */
  threadKey: string;
}

export function decorateThreads(messages: MessageSummary[]): DecoratedThread[] {
  const serverGrouped = messages.some((m) => m.thread_id);
  const countByThread = new Map<number, number>();
  if (serverGrouped) {
    for (const m of messages) {
      if (m.thread_id != null)
        countByThread.set(
          m.thread_id,
          (countByThread.get(m.thread_id) ?? 0) + 1,
        );
    }
    return messages.map((message) => ({
      message,
      threadCount:
        message.thread_id != null
          ? (countByThread.get(message.thread_id) ?? 1)
          : 1,
      threadKey: String(message.thread_id ?? message.uid),
    }));
  }
  // groups are keyed by thread ROOT uid; flatten to per-message counts/keys.
  const countByUid = new Map<number, number>();
  const rootByUid = new Map<number, number>();
  for (const group of groupThreads(messages).values()) {
    for (const m of group.messages) {
      countByUid.set(m.uid, group.messages.length);
      rootByUid.set(m.uid, Number(group.threadId));
    }
  }
  return messages.map((message) => ({
    message,
    threadCount: countByUid.get(message.uid) ?? 1,
    threadKey: String(rootByUid.get(message.uid) ?? message.uid),
  }));
}
