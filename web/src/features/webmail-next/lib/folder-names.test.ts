import { describe, it, expect } from "vitest";
import { decodeFolderName, getFolderDisplayName } from "./folder-names";
import { SCHEDULED_VIEW } from "./special-folders";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

describe("decodeFolderName", () => {
  it("decodes INBOX encoded with b64 prefix", () => {
    expect(decodeFolderName("b64.SU5CT1g")).toBe("INBOX");
  });

  it("decodes Chinese folder names correctly", () => {
    // 5pS25Lu2566x is base64url for "收件箱"
    expect(decodeFolderName("b64.5pS25Lu2566x")).toBe("收件箱");
    // 6YeN6KaB6YKu5Lu2 is base64url for "重要邮件"
    expect(decodeFolderName("b64.6YeN6KaB6YKu5Lu2")).toBe("重要邮件");
    // 5bel5L2c is base64url for "工作"
    expect(decodeFolderName("b64.5bel5L2c")).toBe("工作");
  });

  it("decodes folder names with spaces", () => {
    // UHJvamVjdCBBbHBoYQ is base64url for "Project Alpha" (length 18, needs 2 padding chars)
    expect(decodeFolderName("b64.UHJvamVjdCBBbHBoYQ")).toBe("Project Alpha");
    // TXkgRm9sZGVy is base64url for "My Folder"
    expect(decodeFolderName("b64.TXkgRm9sZGVy")).toBe("My Folder");
  });

  it("returns plaintext unchanged if not starting with b64.", () => {
    expect(decodeFolderName("INBOX")).toBe("INBOX");
    expect(decodeFolderName("收件箱")).toBe("收件箱");
    expect(decodeFolderName("Project Alpha")).toBe("Project Alpha");
    expect(decodeFolderName("Folder/Subfolder")).toBe("Folder/Subfolder");
  });

  it("fails safe on invalid base64 or corrupt payload", () => {
    expect(decodeFolderName("b64.invalid!!!")).toBe("b64.invalid!!!");
    expect(decodeFolderName("b64.A")).toBe("b64.A");
    expect(decodeFolderName("b64.")).toBe("");
  });

  it("fails safe on invalid UTF-8 sequences", () => {
    // "/w" decodes to byte 0xFF, which is invalid UTF-8
    expect(decodeFolderName("b64./w")).toBe("b64./w");
  });

  it("rejects decoded names containing null bytes and returns original string", () => {
    // "hello\0world" in base64url is "aGVsbG8Ad29ybGQ"
    expect(decodeFolderName("b64.aGVsbG8Ad29ybGQ")).toBe("b64.aGVsbG8Ad29ybGQ");
  });

  it("handles non-string or empty inputs gracefully", () => {
    expect(decodeFolderName("")).toBe("");
    // @ts-expect-error test invalid runtime input
    expect(decodeFolderName(null)).toBe("");
    // @ts-expect-error test invalid runtime input
    expect(decodeFolderName(undefined)).toBe("");
  });
});

describe("getFolderDisplayName", () => {
  const mockFolders: MailboxFolder[] = [
    {
      name: "INBOX",
      name_enc: "b64.SU5CT1g",
      delimiter: "/",
      flags: ["\\Inbox"],
    },
    {
      name: "Drafts",
      name_enc: "b64.RHJhZnRz",
      delimiter: "/",
      flags: ["\\Drafts"],
    },
    {
      name: "Sent",
      name_enc: "b64.U2VudA",
      delimiter: "/",
      flags: ["\\Sent"],
    },
    {
      name: "Trash",
      name_enc: "b64.VHJhc2g",
      delimiter: "/",
      flags: ["\\Trash"],
    },
    {
      name: "Archive",
      name_enc: "b64.QXJjaGl2ZQ",
      delimiter: "/",
      flags: ["\\Archive"],
    },
    {
      name: "项目",
      name_enc: "b64.6aG555uu",
      delimiter: "/",
      flags: [],
    },
  ];

  const t = (key: string) => {
    const translations: Record<string, string> = {
      "webmail_next.inbox": "收件箱",
      "webmail_next.drafts": "草稿箱",
      "webmail_next.sent": "已发送",
      "webmail_next.junk": "垃圾邮件",
      "webmail_next.trash": "废纸篓",
      "webmail_next.archive": "归档",
      "webmail_next.scheduled": "定时发送",
    };
    return translations[key] ?? key;
  };

  it("maps system folders to localized name using translation", () => {
    expect(getFolderDisplayName("b64.SU5CT1g", mockFolders, t)).toBe("收件箱");
    expect(getFolderDisplayName("INBOX", mockFolders, t)).toBe("收件箱");
    expect(getFolderDisplayName("b64.RHJhZnRz", mockFolders, t)).toBe("草稿箱");
    expect(getFolderDisplayName("b64.U2VudA", mockFolders, t)).toBe("已发送");
    expect(getFolderDisplayName("b64.VHJhc2g", mockFolders, t)).toBe("废纸篓");
    expect(getFolderDisplayName("b64.QXJjaGl2ZQ", mockFolders, t)).toBe("归档");
  });

  it("resolves SCHEDULED_VIEW to scheduled translation", () => {
    expect(getFolderDisplayName(SCHEDULED_VIEW, mockFolders, t)).toBe(
      "定时发送",
    );
  });

  it("resolves custom folders to plaintext name", () => {
    expect(getFolderDisplayName("b64.6aG555uu", mockFolders, t)).toBe("项目");
    expect(getFolderDisplayName("项目", mockFolders, t)).toBe("项目");
  });

  it("decodes encoded folder names when folder list is empty (loading state)", () => {
    expect(getFolderDisplayName("b64.SU5CT1g", [], t)).toBe("收件箱");
    expect(getFolderDisplayName("b64.5bel5L2c", [], t)).toBe("工作");
    expect(getFolderDisplayName("b64.UHJvamVjdCBBbHBoYQ", [], t)).toBe(
      "Project Alpha",
    );
  });

  it("falls back to decoded name when translation function is omitted", () => {
    expect(getFolderDisplayName("b64.SU5CT1g", mockFolders)).toBe("INBOX");
    expect(getFolderDisplayName("b64.6aG555uu", mockFolders)).toBe("项目");
  });
});
