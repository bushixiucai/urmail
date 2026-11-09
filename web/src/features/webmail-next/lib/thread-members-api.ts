// web/src/features/webmail-next/lib/thread-members-api.ts
import type { MessageSummary } from "@/features/webmail/mailbox-api";
import { mailboxRequest } from "@/features/webmail/mailbox-api";

/**
 * Cross-folder thread-members client (THREAD-MEMBERS-API-CONTRACT v1).
 * The endpoint is anchored on any known member (folder + uid); list rows
 * pass their root uid as {uid} since thread_id is the in-folder root.
 */

export const THREAD_LOAD_STEP = 20;

export interface ThreadMemberSummary extends MessageSummary {
  /** Owning folder of this member (Sent members carry their own path). */
  folder: string;
  folder_enc: string;
  uid_validity: number;
}

export interface ThreadMembersResponse {
  /** In-folder root uid, same value space as the list's thread_id. */
  thread_id: number;
  /** Full member count (incl. Sent, incl. unloaded pages). */
  total: number;
  /** Unread count across origin-folder members only (Sent always read). */
  unread_count: number;
  scope: "server" | "folder" | "bounded";
  /** date ascending (stack order) — the frontend must not re-sort. */
  messages: ThreadMemberSummary[];
  /** false → the summary bar is done (thread start reached). */
  has_more_older: boolean;
  /** Opaque keyset cursor for "load earlier": echo back as `before`. */
  oldest?: string;
  /** True when members newer than this window exist (deep anchor). */
  has_more_newer?: boolean;
  /** Count of unloaded newer members; "更早的" = total-loaded-newer_count. */
  newer_count?: number;
}

export interface FetchThreadMembersOptions {
  limit?: number;
  /** Window anchor: the selected member (kept inside the window). */
  anchorUid?: number;
  /** "Load earlier" cursor from a previous response's `oldest`. */
  before?: string;
  signal?: AbortSignal;
}

export async function fetchThreadMembers(
  originFolderEnc: string,
  originUid: number,
  options: FetchThreadMembersOptions = {},
): Promise<ThreadMembersResponse> {
  const { limit, anchorUid, before, signal } = options;
  const params = new URLSearchParams();
  if (limit != null) params.set("limit", String(limit));
  if (anchorUid != null) params.set("anchor_uid", String(anchorUid));
  if (before) params.set("before", before);
  const qs = params.toString();
  return mailboxRequest<ThreadMembersResponse>(
    `/api/v1/mailbox/folders/${encodeURIComponent(originFolderEnc)}/messages/${originUid}/thread${qs ? `?${qs}` : ""}`,
    { signal },
  );
}
