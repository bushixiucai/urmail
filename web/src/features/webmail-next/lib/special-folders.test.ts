import { describe, expect, it } from "vitest";
import { resolveSpecialFolder } from "./special-folders";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

const folders: MailboxFolder[] = [
  { name: "INBOX", name_enc: "inbox-enc", delimiter: "/", flags: ["\\Inbox"] },
  {
    name: "Deleted Items",
    name_enc: "trash-enc",
    delimiter: "/",
    flags: ["\\Trash"],
  },
  { name: "Archive", name_enc: "archive-enc", delimiter: "/" },
];

describe("resolveSpecialFolder", () => {
  it("prefers the special-use flag over a name", () => {
    expect(resolveSpecialFolder(folders, "\\Trash", "Trash")?.name_enc).toBe(
      "trash-enc",
    );
  });

  it("falls back to a folder name and returns null when absent", () => {
    expect(resolveSpecialFolder(folders, "\\Archive", "Archive")?.name).toBe(
      "Archive",
    );
    expect(resolveSpecialFolder(folders, "\\Junk", "Junk")).toBeNull();
  });
});
