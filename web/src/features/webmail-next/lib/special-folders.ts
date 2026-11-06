import type { MailboxFolder } from "@/features/webmail/mailbox-api";

/** Synthetic navigation id. Not a mailbox name and never passed to listMessages. */
export const SCHEDULED_VIEW = "__scheduled__";

export function folderHasFlag(folder: MailboxFolder, flag: string): boolean {
  const want = flag.toLowerCase();
  return (folder.flags ?? []).some((item) => item.toLowerCase() === want);
}

/**
 * Mirror internal/webmail special-use resolution: flag first, then a
 * case-insensitive folder name. Returns null when the mailbox does not exist.
 * Callers must not invent a name_enc.
 */
export function resolveSpecialFolder(
  folders: MailboxFolder[],
  flag: string,
  fallbackName: string,
): MailboxFolder | null {
  const flagged = folders.find((folder) => folderHasFlag(folder, flag));
  if (flagged) return flagged;
  const want = fallbackName.toLowerCase();
  return (
    folders.find(
      (folder) =>
        folder.name.toLowerCase() === want ||
        folder.name_enc.toLowerCase() === want,
    ) ?? null
  );
}

export function isTrashMailbox(
  folders: MailboxFolder[],
  nameEnc: string,
): boolean {
  const folder = folders.find((item) => item.name_enc === nameEnc);
  if (!folder) return false;
  return (
    folderHasFlag(folder, "\\Trash") || folder.name.toLowerCase() === "trash"
  );
}
