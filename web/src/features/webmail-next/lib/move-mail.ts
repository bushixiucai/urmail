import type { MailboxFolder } from "@/features/webmail/mailbox-api";
import { resolveSpecialFolder } from "./special-folders";

export interface MoveDeps {
  createFolder: (name: string) => Promise<{ folder?: MailboxFolder }>;
  move: (
    sourceEnc: string,
    body: { uids: number[]; to: string; uid_validity?: number },
  ) => Promise<{ destination_uids?: number[] }>;
  reloadFolders: () => Promise<MailboxFolder[]>;
}

export interface MoveUndo {
  sourceEnc: string;
  destinationEnc: string;
  destinationUids: number[];
}

/** Undo only when COPYUID destination_uids align 1:1 with the requested UIDs. */
export function moveUndoFromResponse(
  sourceEnc: string,
  destinationEnc: string,
  requestedUids: number[],
  response: { destination_uids?: number[] } | null | undefined,
): MoveUndo | null {
  const destinationUids = response?.destination_uids;
  if (
    !sourceEnc ||
    !destinationEnc ||
    requestedUids.length === 0 ||
    !destinationUids ||
    destinationUids.length !== requestedUids.length
  ) {
    return null;
  }
  return {
    sourceEnc,
    destinationEnc,
    destinationUids: [...destinationUids],
  };
}

export interface SpecialMoveResult {
  name: string;
  nameEnc: string;
  undo: MoveUndo | null;
}

/**
 * Move UIDs to Archive or Trash. Creates the real mailbox name when missing.
 * Never calls delete. Undo is available only when the response includes a
 * destination_uids list the same length as the request.
 */
export async function moveToSpecialFolder(
  folders: MailboxFolder[],
  sourceEnc: string,
  uids: number[],
  uidValidity: number | undefined,
  flag: "\\Archive" | "\\Trash",
  fallbackName: string,
  deps: MoveDeps,
): Promise<SpecialMoveResult | null> {
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
  const response = await deps.move(sourceEnc, {
    uids,
    to: destination.name,
    uid_validity: uidValidity,
  });
  return {
    name: destination.name,
    nameEnc: destination.name_enc,
    undo: moveUndoFromResponse(sourceEnc, destination.name_enc, uids, response),
  };
}
