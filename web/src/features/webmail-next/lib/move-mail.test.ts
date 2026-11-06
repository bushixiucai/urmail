import { describe, expect, it } from "vitest";
import { moveUndoFromResponse } from "./move-mail";

describe("moveUndoFromResponse", () => {
  it("maps a move back only when destination_uids align", () => {
    expect(
      moveUndoFromResponse("INBOX", "Archive", [101, 102], {
        destination_uids: [880, 881],
      }),
    ).toEqual({
      sourceEnc: "INBOX",
      destinationEnc: "Archive",
      destinationUids: [880, 881],
    });
  });

  it("returns null when the field is omitted or the length differs", () => {
    expect(moveUndoFromResponse("INBOX", "Archive", [101], {})).toBeNull();
    expect(
      moveUndoFromResponse("INBOX", "Archive", [101], {
        destination_uids: [880, 881],
      }),
    ).toBeNull();
    expect(
      moveUndoFromResponse("INBOX", "", [101], { destination_uids: [880] }),
    ).toBeNull();
  });
});
