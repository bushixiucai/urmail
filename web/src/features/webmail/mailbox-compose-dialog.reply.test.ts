import { beforeEach, describe, expect, it } from "vitest";
import { draftFromMessage } from "./mailbox-compose-dialog";
import { setMailboxSession } from "./mailbox-api";
import type { MessageDetail } from "./mailbox-api";

const message: MessageDetail = {
  uid: 1,
  subject: "Hello",
  from: [{ email: "author@example.test" }],
  reply_to: [{ email: "list@example.test" }],
  to: [{ email: "me@example.test" }, { email: "bob@example.test" }],
  cc: [{ email: "cara@example.test" }, { email: "list@example.test" }],
  date: "2026-10-26T00:00:00Z",
  flags: [],
  size: 10,
  text: "body",
  attachments: [],
};

describe("draftFromMessage", () => {
  beforeEach(() => {
    sessionStorage.clear();
    setMailboxSession({
      token: "t",
      address: "me@example.test",
      expires_at: "2099-01-01T00:00:00Z",
    });
  });

  it("replies to Reply-To and does not copy other recipients", () => {
    const draft = draftFromMessage("reply", message);
    expect(draft.to).toBe("list@example.test");
    expect(draft.cc ?? "").toBe("");
  });

  it("reply-all keeps visible people and drops self", () => {
    const draft = draftFromMessage("reply-all", message);
    expect(draft.to).toBe("list@example.test");
    expect(draft.cc).toBe("bob@example.test, cara@example.test");
  });
});
