import { describe, expect, it } from "vitest";
import {
  getFolderKind,
  getFolderInfo,
  sortMailboxFolders,
} from "./mailbox-folder-utils";
import { MailboxFolder } from "./mailbox-api";

describe("mailbox-folder-utils", () => {
  const mockT = (key: string, opts?: { defaultValue?: string }) => {
    const translations: Record<string, string> = {
      "page.webmail.folder_inbox": "收件箱",
      "page.webmail.folder_drafts": "草稿箱",
      "page.webmail.folder_sent": "已发送",
      "page.webmail.folder_junk": "垃圾邮件",
      "page.webmail.folder_trash": "已删除",
      "page.webmail.folder_archive": "归档",
    };
    return translations[key] ?? opts?.defaultValue ?? key;
  };

  it("identifies folder kinds correctly by name and IMAP flags", () => {
    expect(getFolderKind("INBOX")).toBe("inbox");
    expect(getFolderKind("inbox")).toBe("inbox");
    expect(getFolderKind("Custom", ["\\Inbox"])).toBe("inbox");

    expect(getFolderKind("Drafts")).toBe("drafts");
    expect(getFolderKind("Other", ["\\Drafts"])).toBe("drafts");

    expect(getFolderKind("Sent")).toBe("sent");
    expect(getFolderKind("Sent Messages")).toBe("sent");
    expect(getFolderKind("Sent Items")).toBe("sent");
    expect(getFolderKind("Other", ["\\Sent"])).toBe("sent");

    expect(getFolderKind("Trash")).toBe("trash");
    expect(getFolderKind("Deleted")).toBe("trash");
    expect(getFolderKind("Deleted Items")).toBe("trash");
    expect(getFolderKind("Other", ["\\Trash"])).toBe("trash");

    expect(getFolderKind("Junk")).toBe("junk");
    expect(getFolderKind("Spam")).toBe("junk");
    expect(getFolderKind("Other", ["\\Junk"])).toBe("junk");

    expect(getFolderKind("Archive")).toBe("archive");
    expect(getFolderKind("Archives")).toBe("archive");
    expect(getFolderKind("Other", ["\\Archive"])).toBe("archive");

    expect(getFolderKind("Work Projects")).toBe("custom");
  });

  it("returns localized display names and appropriate icons", () => {
    const inbox = getFolderInfo(
      { name: "INBOX", name_enc: "SU5CT1g", delimiter: "/" },
      mockT,
    );
    expect(inbox.displayName).toBe("收件箱");
    expect(inbox.isSystem).toBe(true);

    const drafts = getFolderInfo(
      { name: "Drafts", name_enc: "RHJhZnRz", delimiter: "/" },
      mockT,
    );
    expect(drafts.displayName).toBe("草稿箱");

    const sent = getFolderInfo(
      { name: "Sent", name_enc: "U2VudA", delimiter: "/" },
      mockT,
    );
    expect(sent.displayName).toBe("已发送");

    const custom = getFolderInfo(
      { name: "Project Alpha", name_enc: "YWxwaGE", delimiter: "/" },
      mockT,
    );
    expect(custom.displayName).toBe("Project Alpha");
    expect(custom.isSystem).toBe(false);
  });

  it("sorts system folders in standard order followed by alphabetical custom folders", () => {
    const rawFolders: MailboxFolder[] = [
      { name: "Trash", name_enc: "trash", delimiter: "/" },
      { name: "Z-Custom", name_enc: "z", delimiter: "/" },
      { name: "Sent", name_enc: "sent", delimiter: "/" },
      { name: "INBOX", name_enc: "inbox", delimiter: "/" },
      { name: "A-Custom", name_enc: "a", delimiter: "/" },
      { name: "Drafts", name_enc: "drafts", delimiter: "/" },
      { name: "Junk", name_enc: "junk", delimiter: "/" },
      { name: "Archive", name_enc: "archive", delimiter: "/" },
    ];

    const sorted = sortMailboxFolders(rawFolders, mockT);
    expect(sorted.map((f) => f.name)).toEqual([
      "INBOX",
      "Drafts",
      "Sent",
      "Junk",
      "Trash",
      "Archive",
      "A-Custom",
      "Z-Custom",
    ]);
  });
});
