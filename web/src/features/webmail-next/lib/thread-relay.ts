// web/src/features/webmail-next/lib/thread-relay.ts
import { threadMemberKey } from "./thread-members-api";

export interface RelayMember {
  uid: number;
  folder_enc?: string;
}

/**
 * §11-5 expansion relay target: the next NEWER card after deleting the
 * given member, else the previous older one. Identity is composite
 * (folder_enc:uid) — a numeric uid alone cannot distinguish INBOX 148
 * from Sent 148, which may be adjacent cards in the same thread (QA
 * release blocker on #122).
 */
export function nextStackMemberAfterDelete<T extends RelayMember>(
  ordered: T[],
  deleted: RelayMember,
): T | null {
  const key = threadMemberKey(deleted);
  const idx = ordered.findIndex((m) => threadMemberKey(m) === key);
  if (idx < 0) return null;
  return ordered[idx + 1] ?? ordered[idx - 1] ?? null;
}

/**
 * Composite lookup for stack-target resolution: with a folder hint the
 * match must be exact (same-uid siblings are different mails); members
 * without folder_enc fall back to the origin folder.
 */
export function resolveStackMember<T extends RelayMember>(
  stackMembers: T[],
  uid: number,
  folderEncHint: string | undefined,
  originFolderEnc: string,
): T | undefined {
  return stackMembers.find(
    (m) =>
      m.uid === uid &&
      (folderEncHint == null ||
        (m.folder_enc ?? originFolderEnc) === folderEncHint),
  );
}
