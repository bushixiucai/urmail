import { describe, expect, it, vi } from "vitest";
import { moveUndoFromResponse } from "./move-mail";

describe("moveUndoFromResponse", () => {
  it("maps a move back only when destination_uids align", () => {
    expect(
      moveUndoFromResponse("INBOX", "Archive", [101, 102], {
        destination_uids: [880, 881],
        destination_uid_validity: 12,
      }),
    ).toEqual({
      sourceEnc: "INBOX",
      destinationEnc: "Archive",
      destinationUidValidity: 12,
      destinationUids: [880, 881],
    });
  });

  it("returns null when uids or the destination generation are not verifiable", () => {
    expect(moveUndoFromResponse("INBOX", "Archive", [101], {})).toBeNull();
    expect(
      moveUndoFromResponse("INBOX", "Archive", [101], {
        destination_uids: [880, 881],
        destination_uid_validity: 12,
      }),
    ).toBeNull();
    expect(
      moveUndoFromResponse("INBOX", "", [101], {
        destination_uids: [880],
        destination_uid_validity: 12,
      }),
    ).toBeNull();
    // COPYUID uids present but generation missing → no undo (contract).
    expect(
      moveUndoFromResponse("INBOX", "Archive", [101], {
        destination_uids: [880],
      }),
    ).toBeNull();
  });
});

import { moveToSpecialFolder } from "./move-mail";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

describe("moveToSpecialFolder cross-box (same-uid member in Sent)", () => {
  const folders = [
    { name: "INBOX", name_enc: "b64.SU5CT1g", flags: [] },
    { name: "Sent", name_enc: "b64.U2VudA", flags: ["\\Sent"] },
    { name: "Trash", name_enc: "b64.VHJhc2g", flags: ["\\Trash"] },
  ] as unknown as MailboxFolder[];

  it("moves the SENT copy by its own folder+uid and keeps undo on that box", async () => {
    const move = vi.fn().mockResolvedValue({
      destination_uids: [900],
      destination_uid_validity: 11,
    });
    const result = await moveToSpecialFolder(
      folders,
      "b64.U2VudA", // member's own folder, NOT the origin INBOX
      [148], // same numeric uid as an INBOX mail exists — must stay box-scoped
      7, // member uid_validity
      "\\Trash",
      "Trash",
      {
        createFolder: vi.fn(),
        move,
        reloadFolders: vi.fn().mockResolvedValue(folders),
      },
    );
    expect(move).toHaveBeenCalledWith("b64.U2VudA", {
      uids: [148],
      to: "Trash", // folder name; the api layer encodes it
      uid_validity: 7,
    });
    // Undo must restore INTO the member's own box.
    expect(result?.undo).toEqual({
      sourceEnc: "b64.U2VudA",
      destinationEnc: "b64.VHJhc2g",
      destinationUidValidity: 11,
      destinationUids: [900],
    });
  });
});
