import { expect, it } from "vitest";
import { printableMessageText } from "./mailbox-message-tools";
it("prints inert readable text without script, CSS or external image URLs", () => {
  const text = printableMessageText({
    uid: 1,
    uid_validity: 9,
    subject: "Literal <b>subject</b>",
    from: [{ email: "owner@example.test" }],
    date: "2026-10-27T00:00:00Z",
    flags: [],
    size: 1,
    attachments: [],
    html: '<p>Readable invoice</p><script>fetch("https://tracker.test/token")</script><style>@import "https://tracker.test/css";</style><img src="https://tracker.test/img">',
  });
  expect(text).toContain("Readable invoice");
  expect(text).toContain("Literal <b>subject</b>");
  expect(text).not.toContain("fetch(");
  expect(text).not.toContain("tracker.test");
});
