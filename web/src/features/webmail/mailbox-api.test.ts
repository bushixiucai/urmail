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
  listFolders,
  MAILBOX_AI_CONFIGURATION_CHANGED,
  mailboxRequest,
  saveMailboxSettings,
  setMailboxLoginRedirectHandler,
  MAX_SEND_ATTACHMENT_BYTES,
  MAX_SEND_TOTAL_ATTACHMENT_BYTES,
  saveDraft,
  scheduleSend,
  sendMessage,
  setMailboxSession,
} from "./mailbox-api";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.clear();
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

it("broadcasts only an empty invalidation event after a successful AI settings update", async () => {
  const changed = vi.fn();
  window.addEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, changed);
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ saved: true, version: 2 }),
    }),
  );
  try {
    await saveMailboxSettings({
      display_name: "",
      signature_text: "",
      signature_html: "",
      expected_version: 1,
      preferences: {
        ai: { use_custom: true, api_key: "synthetic-write-only-key" },
      },
    });
    expect(changed).toHaveBeenCalledTimes(1);
    const event = changed.mock.calls[0][0] as Event;
    expect(event.type).toBe(MAILBOX_AI_CONFIGURATION_CHANGED);
    expect("detail" in event).toBe(false);
    expect("api_key" in event).toBe(false);
  } finally {
    window.removeEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, changed);
  }
});
it("does not announce a failed AI settings write or an update without AI configuration", async () => {
  const changed = vi.fn();
  window.addEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, changed);
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce({
      ok: false,
      status: 409,
      json: async () => ({
        error: { code: "CONFLICT", message: "error.CONFLICT" },
      }),
    })
    .mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ saved: true, version: 2 }),
    });
  vi.stubGlobal("fetch", fetcher);
  const body = {
    display_name: "",
    signature_text: "",
    signature_html: "",
    expected_version: 1,
  };
  try {
    await expect(
      saveMailboxSettings({
        ...body,
        preferences: { ai: { use_custom: false } },
      }),
    ).rejects.toMatchObject({ status: 409 });
    await saveMailboxSettings(body);
    expect(changed).not.toHaveBeenCalled();
  } finally {
    window.removeEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, changed);
  }
});

it("treats an expired session as absent and clears the dead token", () => {
  setMailboxSession({
    token: "expired-token",
    address: "user@domain.test",
    expires_at: new Date(Date.now() - 1000).toISOString(),
  });
  expect(getMailboxToken()).toBe("expired-token");
  expect(hasMailboxSession()).toBe(false);
  // Storage is purged: no doomed requests fire after route guards.
  expect(getMailboxToken()).toBeNull();
  expect(getMailboxExpiresAt()).toBeNull();
  expect(window.localStorage.getItem("urmail.mailbox-token")).toBeNull();
  expect(window.sessionStorage.getItem("urmail.mailbox-token")).toBeNull();
});

it("keeps a live session and a session without an expires_at", () => {
  setMailboxSession({
    token: "live-token",
    address: "user@domain.test",
    expires_at: new Date(Date.now() + 60_000).toISOString(),
  });
  expect(hasMailboxSession()).toBe(true);
  expect(getMailboxToken()).toBe("live-token");

  // Legacy or malformed storage without an expiry still counts as a session;
  // the request-level 401 funnel remains the backstop for those.
  window.localStorage.setItem("urmail.mailbox-token", "no-expiry-token");
  window.localStorage.setItem("urmail.mailbox-address", "user@domain.test");
  window.localStorage.removeItem("urmail.mailbox-expires");
  expect(hasMailboxSession()).toBe(true);
});

it("redirects once on 401 and clears the session (single flight)", async () => {
  setMailboxSession({
    token: "dying-token",
    address: "user@domain.test",
    expires_at: new Date(Date.now() + 60_000).toISOString(),
  });
  const assign = vi.fn();
  setMailboxLoginRedirectHandler(assign);
  const fetcher = vi.fn().mockResolvedValue({
    ok: false,
    status: 401,
    json: async () => ({
      error: {
        code: "MAILBOX_UNAUTHORIZED",
        message: "error.MAILBOX_UNAUTHORIZED",
      },
    }),
  });
  vi.stubGlobal("fetch", fetcher);
  try {
    // Five concurrent authenticated requests race with one dead token.
    await Promise.allSettled([
      listFolders(),
      listFolders(),
      listFolders(),
      listFolders(),
      listFolders(),
    ]);
    expect(assign).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledWith("/webmail/login");
    expect(getMailboxToken()).toBeNull();
  } finally {
    setMailboxLoginRedirectHandler(null);
  }
});

it("does not redirect for non-auth errors or unauthenticated requests", async () => {
  const assign = vi.fn();
  setMailboxLoginRedirectHandler(assign);
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      }),
    })
    .mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ native: true }),
    });
  vi.stubGlobal("fetch", fetcher);
  try {
    await expect(listFolders()).rejects.toMatchObject({ status: 500 });
    expect(assign).not.toHaveBeenCalled();
    expect(getMailboxToken()).toBeNull();
    // An unauthenticated call (login/status) must never trigger the funnel,
    // even when the endpoint itself answers 401.
    setMailboxSession({
      token: "t",
      address: "a@b.test",
      expires_at: new Date(Date.now() + 60_000).toISOString(),
    });
    fetcher.mockResolvedValueOnce({
      ok: false,
      status: 401,
      json: async () => ({
        error: {
          code: "MAILBOX_UNAUTHORIZED",
          message: "error.MAILBOX_UNAUTHORIZED",
        },
      }),
    });
    await expect(
      mailboxRequest("/api/v1/mailbox/status", { auth: false }),
    ).rejects.toMatchObject({ status: 401 });
    expect(assign).not.toHaveBeenCalled();
    expect(getMailboxToken()).toBe("t");
  } finally {
    setMailboxLoginRedirectHandler(null);
  }
});
