// web/src/features/webmail-next/hooks/use-message-threading.ts
import { useMemo, useState, useCallback } from "react";
import type { MessageSummary } from "@/features/webmail/mailbox-api";
import type { WebmailItem } from "../types";

export function useMessageThreading(messages: MessageSummary[]) {
  const [expandedThreadIds, setExpandedThreadIds] = useState<Set<number>>(
    new Set(),
  );

  const toggleThreadExpand = useCallback((threadId: number) => {
    setExpandedThreadIds((prev) => {
      const next = new Set(prev);
      if (next.has(threadId)) {
        next.delete(threadId);
      } else {
        next.add(threadId);
      }
      return next;
    });
  }, []);

  const items = useMemo<WebmailItem[]>(() => {
    if (!Array.isArray(messages)) return [];

    const threadGroups = new Map<number, MessageSummary[]>();
    const standalones: MessageSummary[] = [];

    for (const msg of messages) {
      if (!msg) continue;
      if (msg.thread_id && msg.thread_count && msg.thread_count > 1) {
        const group = threadGroups.get(msg.thread_id) || [];
        group.push(msg);
        threadGroups.set(msg.thread_id, group);
      } else {
        standalones.push(msg);
      }
    }

    const result: WebmailItem[] = [];

    // Helper to format MessageSummary into WebmailItem
    const toWebmailItem = (m: MessageSummary, isChild = false): WebmailItem => {
      const senderObj = m.from?.[0];
      const senderName =
        senderObj?.name || senderObj?.email?.split("@")[0] || "Unknown";
      const flags = Array.isArray(m.flags) ? m.flags : [];
      const isUnread = !flags.includes("\\Seen");
      const isStarred = flags.includes("\\Flagged");

      return {
        uid: m.uid,
        uid_validity: m.uid_validity,
        subject: m.subject || "(No subject)",
        sender: senderName,
        senderEmail: senderObj?.email || "",
        date: m.date,
        snippet: m.preview && m.preview !== m.subject ? m.preview : undefined,
        isUnread,
        isStarred,
        hasAttachments: Boolean(m.has_attachments),
        size: m.size,
        threadId: m.thread_id,
        threadCount: isChild ? undefined : m.thread_count,
        folder: m.folder,
        folderEnc: m.folder_enc,
      };
    };

    // Process threads: take the top message as representative
    for (const [threadId, msgs] of threadGroups.entries()) {
      // Sort msgs newest first
      const sorted = [...msgs].sort(
        (a, b) =>
          new Date(b?.date || 0).getTime() - new Date(a?.date || 0).getTime(),
      );
      const root = sorted[0];
      const isExpanded = expandedThreadIds.has(threadId);

      const parentItem = toWebmailItem(root);
      parentItem.threadCount = msgs.length;
      parentItem.isThreadExpanded = isExpanded;
      parentItem.childUids = sorted.slice(1).map((c) => c.uid);

      // Senders summary
      const uniqueSenders = Array.from(
        new Set(
          sorted
            .map((s) => s.from?.[0]?.name || s.from?.[0]?.email?.split("@")[0])
            .filter(Boolean),
        ),
      );
      parentItem.sender = `${uniqueSenders.join(", ")} (${msgs.length})`;

      result.push(parentItem);

      if (isExpanded) {
        for (const child of sorted.slice(1)) {
          result.push(toWebmailItem(child, true));
        }
      }
    }

    // Process standalones
    for (const msg of standalones) {
      result.push(toWebmailItem(msg));
    }

    return result;
  }, [messages, expandedThreadIds]);

  return {
    items,
    expandedThreadIds,
    toggleThreadExpand,
  };
}
