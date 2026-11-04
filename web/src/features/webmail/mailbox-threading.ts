import type { MessageSummary } from "./mailbox-api";

/**
 * Strips RFC 5256-style reply and forward markers including CJK equivalents.
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

/**
 * Extracts and normalizes message IDs from header strings (In-Reply-To, References).
 * Strips surrounding '<' and '>' and extracts tokens.
 */
export function parseMessageIds(raw?: string | string[]): string[] {
  if (!raw) return [];
  if (Array.isArray(raw)) {
    return raw.flatMap((item) => parseMessageIds(item));
  }
  const str = String(raw).trim();
  if (!str) return [];
  const matches = str.match(/<([^>]+)>/g);
  if (matches && matches.length > 0) {
    return matches.map((m) => m.replace(/^<|>$/g, "").trim()).filter(Boolean);
  }
  return str
    .split(/\s+/)
    .map((s) => s.replace(/^<|>$/g, "").trim())
    .filter(Boolean);
}

export interface ThreadGroup {
  threadId: string;
  messages: MessageSummary[];
}

/**
 * Groups messages into conversations strictly based on Message-ID,
 * In-Reply-To, and References tree parsing.
 * RFC 5256 / Roundcube parity rule (I04): Never merge purely by subject.
 */
export function groupThreads(
  messages: MessageSummary[],
): Map<number, ThreadGroup> {
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

  const sorted = [...messages].sort((a, b) => a.uid - b.uid);
  for (const m of sorted) {
    parent.set(m.uid, m.uid);
  }

  // Map known Message-IDs to their message UID.
  const idToUid = new Map<string, number>();
  // Map referenced Message-IDs (including unseen ancestors) to the first message referencing them.
  const refToFirstUid = new Map<string, number>();

  for (const m of sorted) {
    const mid = (m.message_id ?? "").replace(/^<|>$/g, "").trim();
    if (mid) {
      idToUid.set(mid, m.uid);
    }
  }

  for (const m of sorted) {
    const mid = (m.message_id ?? "").replace(/^<|>$/g, "").trim();
    // In-Reply-To IDs
    const inReplyToIds = parseMessageIds(m.in_reply_to);
    // References IDs (check both summary field and any extended property)
    const rawRefs = (m as { references?: string | string[] }).references;
    const refIds = parseMessageIds(rawRefs);

    const relatedIds = Array.from(new Set([...inReplyToIds, ...refIds]));

    for (const refId of relatedIds) {
      if (!refId) continue;
      // 1. Direct parent/ancestor match
      const targetUid = idToUid.get(refId);
      if (targetUid != null) {
        union(m.uid, targetUid);
      }
      // 2. Siblings sharing the same reference ancestor even if missing locally
      const siblingUid = refToFirstUid.get(refId);
      if (siblingUid != null) {
        union(m.uid, siblingUid);
      } else {
        refToFirstUid.set(refId, m.uid);
      }
    }

    // If mid was already referenced by an earlier message before this message was registered
    if (mid && refToFirstUid.has(mid)) {
      union(m.uid, refToFirstUid.get(mid)!);
    }
  }

  const byUid = new Map<number, number>();
  for (const m of sorted) {
    byUid.set(m.uid, find(m.uid));
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

export interface DecoratedThread {
  message: MessageSummary;
  threadCount: number;
  /** Stable key for the message's thread (root uid or server thread_id). */
  threadKey: string;
}

/**
 * UI grouping: returns (message, threadCount, threadKey) triples.
 * Accurate conversation count: respects server-side thread_count when available,
 * not bounded by current page count.
 */
export function decorateThreads(messages: MessageSummary[]): DecoratedThread[] {
  const serverGrouped = messages.some((m) => m.thread_id != null);
  const countByThread = new Map<number, number>();

  if (serverGrouped) {
    for (const m of messages) {
      if (m.thread_id != null) {
        countByThread.set(
          m.thread_id,
          (countByThread.get(m.thread_id) ?? 0) + 1,
        );
      }
    }
    return messages.map((message) => {
      const localCount =
        message.thread_id != null
          ? (countByThread.get(message.thread_id) ?? 1)
          : 1;
      const accurateCount = Math.max(message.thread_count ?? 1, localCount);
      return {
        message,
        threadCount: accurateCount,
        threadKey: String(message.thread_id ?? message.uid),
      };
    });
  }

  // Client-side grouping via groupThreads
  const groups = groupThreads(messages);
  const countByUid = new Map<number, number>();
  const rootByUid = new Map<number, number>();

  for (const group of groups.values()) {
    for (const m of group.messages) {
      const accurateCount = Math.max(
        m.thread_count ?? 1,
        group.messages.length,
      );
      countByUid.set(m.uid, accurateCount);
      rootByUid.set(m.uid, Number(group.threadId));
    }
  }

  return messages.map((message) => ({
    message,
    threadCount: countByUid.get(message.uid) ?? 1,
    threadKey: String(rootByUid.get(message.uid) ?? message.uid),
  }));
}
