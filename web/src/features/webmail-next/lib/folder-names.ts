import type { MailboxFolder } from "@/features/webmail/mailbox-api";
import { folderHasFlag, SCHEDULED_VIEW } from "./special-folders";

/**
 * System folder configurations matching backend special-use flags,
 * fallback folder names, and frontend translation keys.
 */
export const SYSTEM_FOLDER_CONFIGS = [
  { flag: "\\Inbox", fallback: "INBOX", labelKey: "webmail_next.inbox" },
  { flag: "\\Drafts", fallback: "Drafts", labelKey: "webmail_next.drafts" },
  { flag: "\\Sent", fallback: "Sent", labelKey: "webmail_next.sent" },
  { flag: "\\Junk", fallback: "Junk", labelKey: "webmail_next.junk" },
  { flag: "\\Trash", fallback: "Trash", labelKey: "webmail_next.trash" },
  { flag: "\\Archive", fallback: "Archive", labelKey: "webmail_next.archive" },
] as const;

/**
 * Decodes a folder name encoded with the backend contract:
 * "b64." + base64.RawURLEncoding.EncodeToString([]byte(name))
 *
 * Rules:
 * - If not a string, returns empty string.
 * - If name does not start with "b64.", returns original name.
 * - Handles base64url (- and _) and missing padding (=).
 * - Decodes UTF-8 bytes with TextDecoder("utf-8", { fatal: true }).
 * - Rejects strings containing null bytes (\0) by falling back to the original string.
 * - On any decoding or format error, fail-safe returns the original string.
 */
export function decodeFolderName(name: string): string {
  if (typeof name !== "string") return "";
  if (!name.startsWith("b64.")) return name;

  try {
    const raw = name.slice(4).replace(/-/g, "+").replace(/_/g, "/");
    const pad = (4 - (raw.length % 4)) % 4;
    const padded = raw + "=".repeat(pad);
    const bin = atob(padded);
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
    const decoded = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    if (decoded.includes("\0")) return name;
    return decoded;
  } catch {
    return name;
  }
}

/**
 * Resolves a folder's user-facing display name.
 *
 * Checks:
 * 1. Synthetic views like SCHEDULED_VIEW.
 * 2. Decodes base64 name if encoded.
 * 3. Matches system folders by flag or well-known name, translating via `t` if provided.
 * 4. Resolves custom folder plaintext name from folders list or decoded string.
 */
export function getFolderDisplayName(
  folderEncOrName: string,
  folders: MailboxFolder[] = [],
  t?: (key: string) => string,
): string {
  if (typeof folderEncOrName !== "string" || !folderEncOrName) {
    return "";
  }

  if (folderEncOrName === SCHEDULED_VIEW) {
    return t ? t("webmail_next.scheduled") : "Scheduled";
  }

  const decoded = decodeFolderName(folderEncOrName);

  // Match folder in folder list by name_enc or name or decoded name
  const matched = folders.find(
    (f) =>
      f.name_enc === folderEncOrName ||
      f.name === folderEncOrName ||
      f.name === decoded,
  );

  // Check system folders
  for (const sys of SYSTEM_FOLDER_CONFIGS) {
    const isFlagMatch = matched && folderHasFlag(matched, sys.flag);
    const isFallbackMatch =
      matched?.name?.toLowerCase() === sys.fallback.toLowerCase() ||
      decoded.toLowerCase() === sys.fallback.toLowerCase();

    if (isFlagMatch || isFallbackMatch) {
      return t ? t(sys.labelKey) : matched?.name || decoded;
    }
  }

  if (matched?.name) {
    return decodeFolderName(matched.name);
  }

  return decoded;
}
