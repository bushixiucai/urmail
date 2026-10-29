import { afterEach, expect, it, vi } from "vitest";
import {
  attachmentDecodedBytes,
  attachmentTotalBytes,
  assertAttachmentTotal,
  clearMailboxSession,
  getMailboxAddress,
  getMailboxExpiresAt,
  getMailboxToken,
  hasMailboxSession,
  MAX_SEND_ATTACHMENT_BYTES,
  MAX_SEND_TOTAL_ATTACHMENT_BYTES,
  saveDraft,
  scheduleSend,
  sendMessage,
  setMailboxSession,
} from "./mailbox-api";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});
it("binds schedule retries to the idempotency header and exact UIDVALIDITY", async () => {
  window.sessionStorage.setItem("urmail.mailbox-token", "mailbox-only");
  const fetcher = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ id: 1 }),
  });
  vi.stubGlobal("fetch", fetcher);
  const body = {
    draft_uid: 22,
    draft_uidvalidity: 9,
    send_at: "2099-01-01T00:00:00Z",
  };
  await scheduleSend(body, "replay-key-1234567890");
  await scheduleSend(body, "replay-key-1234567890");
  expect(fetcher.mock.calls[0]).toEqual(fetcher.mock.calls[1]);
  expect(fetcher.mock.calls[0][1].headers).toMatchObject({
    Authorization: "Bearer mailbox-only",
    "Idempotency-Key": "replay-key-1234567890",
  });
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual(body);
});

function sizedAttachment(size: number) {
  const padding = (3 - (size % 3)) % 3;
  return {
    filename: "bytes.bin",
    content_type: "application/octet-stream",
    content_base64:
      "A".repeat(Math.ceil(size / 3) * 4 - padding) + "=".repeat(padding),
  };
}

it("counts decoded aggregate bytes including base64 padding", () => {
  for (const size of [0, 1, 2, 3, 4, MAX_SEND_ATTACHMENT_BYTES]) {
    expect(attachmentDecodedBytes(sizedAttachment(size))).toBe(size);
  }
  const pair = [
    sizedAttachment(MAX_SEND_ATTACHMENT_BYTES),
    sizedAttachment(MAX_SEND_ATTACHMENT_BYTES),
  ];
  expect(attachmentTotalBytes(pair)).toBe(MAX_SEND_TOTAL_ATTACHMENT_BYTES);
  expect(() => assertAttachmentTotal(pair)).not.toThrow();
  expect(() => assertAttachmentTotal([...pair, sizedAttachment(1)])).toThrow();
});

it("rejects an oversized aggregate before sending or replacing a draft", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  const attachments = [
    sizedAttachment(MAX_SEND_ATTACHMENT_BYTES),
    sizedAttachment(MAX_SEND_ATTACHMENT_BYTES),
    sizedAttachment(1),
  ];
  const body = {
    to: ["bob@example.test"],
    subject: "large",
    text: "body",
    attachments,
  };
  await expect(sendMessage(body)).rejects.toMatchObject({
    status: 413,
    code: "MAILBOX_MESSAGE_TOO_LARGE",
  });
  await expect(
    saveDraft({ ...body, uid: 7, uid_validity: 9 }),
  ).rejects.toMatchObject({ status: 413, code: "MAILBOX_MESSAGE_TOO_LARGE" });
  expect(fetcher).not.toHaveBeenCalled();
});

it("preserves the server aggregate size error in the standard API error path", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: false,
      status: 413,
      json: async () => ({
        error: {
          code: "MAILBOX_MESSAGE_TOO_LARGE",
          message: "error.MAILBOX_MESSAGE_TOO_LARGE",
        },
      }),
    }),
  );
  await expect(
    sendMessage({ to: ["bob@example.test"], subject: "body", text: "body" }),
  ).rejects.toMatchObject({ status: 413, code: "MAILBOX_MESSAGE_TOO_LARGE" });
});

it("handles mailbox session persistence, remember me, and clearMailboxSession", () => {
  const session = {
    token: "mb-token-123",
    address: "user@domain.test",
    expires_at: "2099-12-31T23:59:59Z",
  };

  expect(hasMailboxSession()).toBe(false);

  // default remember=true writes to localStorage
  setMailboxSession(session);
  expect(hasMailboxSession()).toBe(true);
  expect(getMailboxToken()).toBe("mb-token-123");
  expect(getMailboxAddress()).toBe("user@domain.test");
  expect(getMailboxExpiresAt()).toBe("2099-12-31T23:59:59Z");
  expect(window.localStorage.getItem("urmail.mailbox-token")).toBe(
    "mb-token-123",
  );
  expect(window.sessionStorage.getItem("urmail.mailbox-token")).toBeNull();

  // remember=false writes to sessionStorage
  setMailboxSession(session, false);
  expect(window.sessionStorage.getItem("urmail.mailbox-token")).toBe(
    "mb-token-123",
  );
  expect(window.localStorage.getItem("urmail.mailbox-token")).toBeNull();
  expect(hasMailboxSession()).toBe(true);

  // fallback to sessionStorage when not in localStorage
  expect(getMailboxToken()).toBe("mb-token-123");
  expect(getMailboxAddress()).toBe("user@domain.test");
  expect(getMailboxExpiresAt()).toBe("2099-12-31T23:59:59Z");

  // clearMailboxSession purges both
  window.localStorage.setItem("urmail.mailbox-token", "stale");
  clearMailboxSession();
  expect(hasMailboxSession()).toBe(false);
  expect(getMailboxToken()).toBeNull();
  expect(getMailboxAddress()).toBeNull();
  expect(getMailboxExpiresAt()).toBeNull();
  expect(window.localStorage.getItem("urmail.mailbox-token")).toBeNull();
  expect(window.sessionStorage.getItem("urmail.mailbox-token")).toBeNull();
});
