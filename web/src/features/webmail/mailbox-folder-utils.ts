import {
  Inbox,
  FileText,
  Send,
  AlertOctagon,
  Trash2,
  Archive,
  Folder as FolderIcon,
  LucideIcon,
} from "lucide-react";
import { MailboxFolder } from "./mailbox-api";

export type FolderKind =
  "inbox" | "drafts" | "sent" | "junk" | "trash" | "archive" | "custom";

export interface FolderDisplayInfo {
  kind: FolderKind;
  displayName: string;
  orderWeight: number;
  icon: LucideIcon;
  isSystem: boolean;
}

export function getFolderKind(name: string, flags: string[] = []): FolderKind {
  const n = name.trim().toLowerCase();
  const lowerFlags = flags.map((f) => f.toLowerCase());

  if (n === "inbox" || lowerFlags.includes("\\inbox")) {
    return "inbox";
  }
  if (n === "drafts" || lowerFlags.includes("\\drafts")) {
    return "drafts";
  }
  if (
    n === "sent" ||
    n === "sent messages" ||
    n === "sent items" ||
    lowerFlags.includes("\\sent")
  ) {
    return "sent";
  }
  if (n === "junk" || n === "spam" || lowerFlags.includes("\\junk")) {
    return "junk";
  }
  if (
    n === "trash" ||
    n === "deleted" ||
    n === "deleted messages" ||
    n === "deleted items" ||
    lowerFlags.includes("\\trash")
  ) {
    return "trash";
  }
  if (n === "archive" || n === "archives" || lowerFlags.includes("\\archive")) {
    return "archive";
  }
  return "custom";
}

function resolveDisplayName(
  t: (key: string, opts?: { defaultValue?: string }) => string,
  key: string,
  fallback: string,
): string {
  const tr = t(key);
  if (tr && tr !== key) return tr;
  return fallback;
}

export function getFolderInfo(
  folder: MailboxFolder,
  t: (key: string, opts?: { defaultValue?: string }) => string,
): FolderDisplayInfo {
  const kind = getFolderKind(folder.name, folder.flags ?? []);

  switch (kind) {
    case "inbox":
      return {
        kind,
        displayName: resolveDisplayName(
          t,
          "page.webmail.folder_inbox",
          folder.name,
        ),
        orderWeight: 10,
        icon: Inbox,
        isSystem: true,
      };
    case "drafts":
      return {
        kind,
        displayName: resolveDisplayName(
          t,
          "page.webmail.folder_drafts",
          folder.name,
        ),
        orderWeight: 20,
        icon: FileText,
        isSystem: true,
      };
    case "sent":
      return {
        kind,
        displayName: resolveDisplayName(
          t,
          "page.webmail.folder_sent",
          folder.name,
        ),
        orderWeight: 30,
        icon: Send,
        isSystem: true,
      };
    case "junk":
      return {
        kind,
        displayName: resolveDisplayName(
          t,
          "page.webmail.folder_junk",
          folder.name,
        ),
        orderWeight: 40,
        icon: AlertOctagon,
        isSystem: true,
      };
    case "trash":
      return {
        kind,
        displayName: resolveDisplayName(
          t,
          "page.webmail.folder_trash",
          folder.name,
        ),
        orderWeight: 50,
        icon: Trash2,
        isSystem: true,
      };
    case "archive":
      return {
        kind,
        displayName: resolveDisplayName(
          t,
          "page.webmail.folder_archive",
          folder.name,
        ),
        orderWeight: 60,
        icon: Archive,
        isSystem: true,
      };
    case "custom":
    default:
      return {
        kind: "custom",
        displayName: folder.name,
        orderWeight: 100,
        icon: FolderIcon,
        isSystem: false,
      };
  }
}

export function sortMailboxFolders(
  folders: MailboxFolder[],
  t?: (key: string, opts?: { defaultValue?: string }) => string,
): MailboxFolder[] {
  const translate = t ?? ((_k, o) => o?.defaultValue ?? _k);
  return [...folders].sort((a, b) => {
    const infoA = getFolderInfo(a, translate);
    const infoB = getFolderInfo(b, translate);
    if (infoA.orderWeight !== infoB.orderWeight) {
      return infoA.orderWeight - infoB.orderWeight;
    }
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
}
