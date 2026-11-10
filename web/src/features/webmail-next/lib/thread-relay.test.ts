import { describe, expect, it } from "vitest";
import { nextStackMemberAfterDelete } from "./thread-relay";

const INBOX_148 = { uid: 148, folder_enc: "b64.SU5CT1g" };
const SENT_148 = { uid: 148, folder_enc: "b64.U2VudA" };

describe("nextStackMemberAfterDelete (same-uid cross-box relay)", () => {
  it("relays to the Sent sibling when deleting the INBOX member of the same uid", () => {
    const ordered = [INBOX_148, SENT_148];
    expect(nextStackMemberAfterDelete(ordered, INBOX_148)).toBe(SENT_148);
    expect(nextStackMemberAfterDelete(ordered, SENT_148)).toBe(INBOX_148);
  });

  it("does not mistake a same-uid sibling for the deleted member", () => {
    // Deleting Sent 148 must NOT return null just because INBOX 148 shares
    // the numeric uid (the pre-fix `next.uid !== uid` guard failed here).
    const ordered = [INBOX_148, SENT_148];
    const next = nextStackMemberAfterDelete(ordered, SENT_148);
    expect(next?.folder_enc).toBe("b64.SU5CT1g");
  });

  it("prefers the newer neighbor, falls back to the older one", () => {
    const a = { uid: 1, folder_enc: "f" };
    const b = { uid: 2, folder_enc: "f" };
    const c = { uid: 3, folder_enc: "f" };
    expect(nextStackMemberAfterDelete([a, b, c], b)).toBe(c);
    expect(nextStackMemberAfterDelete([a, b, c], c)).toBe(b);
    expect(nextStackMemberAfterDelete([a], a)).toBeNull();
  });

  it("supports members without folder_enc (single-folder fallback)", () => {
    const a = { uid: 1 };
    const b = { uid: 2 };
    expect(nextStackMemberAfterDelete([a, b], a)).toBe(b);
  });
});

import { resolveStackMember } from "./thread-relay";

describe("resolveStackMember (workbench target resolution)", () => {
  const inbox = { uid: 148, folder_enc: "b64.SU5CT1g", body: "INBOX copy" };
  const sent = { uid: 148, folder_enc: "b64.U2VudA", body: "Sent copy" };
  const members = [inbox, sent];

  it("selects the exact copy when the stack passes folderEnc", () => {
    expect(resolveStackMember(members, 148, "b64.U2VudA", "b64.SU5CT1g")).toBe(
      sent,
    );
    expect(resolveStackMember(members, 148, "b64.SU5CT1g", "b64.SU5CT1g")).toBe(
      inbox,
    );
  });

  it("never guesses another box's copy without a hint", () => {
    // Origin member exists → picked; origin member absent → undefined
    // (caller falls back to the list row, never to the Sent copy).
    expect(resolveStackMember(members, 148, undefined, "b64.SU5CT1g")).toBe(
      inbox,
    );
    expect(
      resolveStackMember([sent], 148, undefined, "b64.SU5CT1g"),
    ).toBeUndefined();
  });

  it("matches members without folder_enc against the origin folder", () => {
    const bare = { uid: 7 };
    expect(resolveStackMember([bare], 7, "INBOX", "INBOX")).toBe(bare);
  });
});
