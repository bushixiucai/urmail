import type { MailboxFolder } from "@/features/webmail/mailbox-api";
import { resolveSpecialFolder } from "./special-folders";

export interface MoveDeps {
  createFolder: (name: string) => Promise<{ folder?: MailboxFolder }>;
  move: (
    sourceEnc: string,
    body: { uids: number[]; to: string; uid_validity?: number },
  ) => Promise<unknown>;
  reloadFolders: () => Promise<MailboxFolder[]>;
}

/**
 * Move UIDs to Archive or Trash. Creates the real mailbox name when missing.
 * Never calls delete, and never offers undo: the move response has no new UIDs.
 */
export async function moveToSpecialFolder(
  folders: MailboxFolder[],
  sourceEnc: string,
  uids: number[],
  uidValidity: number | undefined,
  flag: "\\Archive" | "\\Trash",
  fallbackName: string,
  deps: MoveDeps,
): Promise<string | null> {
  if (uids.length === 0 || !uidValidity) return null;
  let destination = resolveSpecialFolder(folders, flag, fallbackName);
  if (!destination) {
    const created = await deps.createFolder(fallbackName);
    destination = created.folder ?? null;
    if (!destination) {
      const refreshed = await deps.reloadFolders();
      destination = resolveSpecialFolder(refreshed, flag, fallbackName);
    }
  }
  if (!destination) return null;
  await deps.move(sourceEnc, {
    uids,
    to: destination.name,
    uid_validity: uidValidity,
  });
  return destination.name;
}
