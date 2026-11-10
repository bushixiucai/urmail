// web/src/features/webmail-next/hooks/use-message-threading.ts
import { useMemo, useState, useCallback } from "react";
import type { MessageSummary } from "@/features/webmail/mailbox-api";
import type { WebmailItem } from "../types";

/** Composite grouping identity: thread_id is a per-folder root UID and two
 * boxes can legally share the same numeric id (Backend audit). Rows without
 * folder_enc keep legacy single-box grouping semantics. */
function groupKeyOf(m: MessageSummary, knownFolderEnc?: string): string {
  // Known browsing folder fills the identity for legacy rows that omit
  // folder_enc (Backend: single-box lists omit it); rows WITH folder_enc
  // always use their own box. Unknown source without any folder context
  // falls back to the legacy single-box key (compat).
  const box = m.folder_enc ?? knownFolderEnc ?? "legacy";
  return `${box}:${m.thread_id ?? 0}`;
}

export function useMessageThreading(
  messages: MessageSummary[],
  knownFolderEnc?: string,
  /** Cross-folder/search context: a row without folder_enc has UNKNOWN
   * provenance — it must stay standalone (no grouping by bare thread_id).
   * Single-box callers keep the legacy "assume browsing box" behavior. */
  unknownProvenanceIsStandalone = false,
) {
  const [expandedThreadIds, setExpandedThreadIds] = useState<Set<string>>(
    new Set(),
  );

  const toggleThreadExpand = useCallback((key: string) => {
    setExpandedThreadIds((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  }, []);

  const items = useMemo<WebmailItem[]>(() => {
    if (!Array.isArray(messages)) return [];

    const threadGroups = new Map<string, MessageSummary[]>();
    const standalones: MessageSummary[] = [];

    for (const msg of messages) {
      if (!msg) continue;
      if (
        unknownProvenanceIsStandalone &&
        msg.folder_enc == null &&
        knownFolderEnc == null
      ) {
        // Unknown provenance in a mixed context: never group.
        standalones.push(msg);
        continue;
      }
      if (msg.thread_id && msg.thread_count && msg.thread_count > 1) {
        const gk = groupKeyOf(msg, knownFolderEnc);
        const group = threadGroups.get(gk) || [];
        group.push(msg);
        threadGroups.set(gk, group);
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
        threadVerification: m.thread_verification,
        threadGroupKey: groupKeyOf(m, knownFolderEnc),
        isThreadChild: isChild || undefined,
        folder: m.folder,
        folderEnc: m.folder_enc,
      };
    };

    // Process threads: take the top message as representative
    for (const [, msgs] of threadGroups.entries()) {
      // Sort msgs newest first
      const sorted = [...msgs].sort(
        (a, b) =>
          new Date(b?.date || 0).getTime() - new Date(a?.date || 0).getTime(),
      );
      const root = sorted[0];
      const groupKey = groupKeyOf(root, knownFolderEnc);
      const isExpanded = expandedThreadIds.has(groupKey);

      const maxServerCount = Math.max(
        ...msgs.map((m) => m.thread_count ?? 0),
        root.thread_count ?? 0,
      );
      const totalCount =
        maxServerCount > msgs.length ? maxServerCount : msgs.length;

      const parentItem = toWebmailItem(root);
      parentItem.threadCount = totalCount;
      parentItem.threadGroupKey = groupKey;
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
      parentItem.sender = `${uniqueSenders.join(", ")} (${totalCount})`;

      result.push(parentItem);

      if (isExpanded) {
        // Spec: children under the parent, oldest → newest (exclude the
        // representative which is the newest message).
        const children = sorted.slice(1).reverse();
        children.forEach((child, index) => {
          const item = toWebmailItem(child, true);
          if (index === children.length - 1) item.isLastThreadChild = true;
          result.push(item);
        });
      }
    }

    // Process standalones
    for (const msg of standalones) {
      result.push(toWebmailItem(msg));
    }

    return result;
  }, [
    messages,
    knownFolderEnc,
    unknownProvenanceIsStandalone,
    expandedThreadIds,
  ]);

  return {
    items,
    expandedThreadIds,
    toggleThreadExpand,
  };
}
