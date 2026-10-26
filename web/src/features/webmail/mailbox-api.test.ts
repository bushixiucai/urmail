import { afterEach, expect, it, vi } from "vitest";
import { scheduleSend } from "./mailbox-api";

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
