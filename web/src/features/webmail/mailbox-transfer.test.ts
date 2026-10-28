import { afterEach, expect, it, vi } from "vitest";
import { mailboxTransfer } from "./mailbox-api";
afterEach(() => {
  sessionStorage.clear();
  vi.unstubAllGlobals();
});
it("reuses the same transfer intent after response loss and page-level retry", async () => {
  sessionStorage.setItem("urmail.mailbox-address", "owner@example.test");
  const fetcher = vi
    .fn()
    .mockRejectedValueOnce(new TypeError("response lost"))
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        results: [{ uid: 7, status: "success", destination_uid: 19 }],
      }),
    })
    .mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        results: [{ uid: 7, status: "success", destination_uid: 20 }],
      }),
    });
  vi.stubGlobal("fetch", fetcher);
  const body = { uids: [7], to: "Archive", uid_validity: 5 };
  await expect(mailboxTransfer("INBOX", "copy", body)).rejects.toThrow(
    "response lost",
  );
  await mailboxTransfer("INBOX", "copy", body);
  const key = (index: number) =>
    (fetcher.mock.calls[index][1].headers as Record<string, string>)[
      "Idempotency-Key"
    ];
  expect(key(0)).toBeTruthy();
  expect(key(1)).toBe(key(0));
  await mailboxTransfer("INBOX", "copy", body);
  expect(key(2)).not.toBe(key(0));
});
it("retains an unknown outcome key and binds a new target to a distinct intent", async () => {
  const fetcher = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ results: [{ uid: 7, status: "unknown" }] }),
  });
  vi.stubGlobal("fetch", fetcher);
  const body = { uids: [7], to: "Archive", uid_validity: 5 };
  await mailboxTransfer("INBOX", "copy", body);
  await mailboxTransfer("INBOX", "copy", body);
  await mailboxTransfer("INBOX", "copy", { ...body, to: "Research" });
  const keys = fetcher.mock.calls.map(
    (c) => (c[1].headers as Record<string, string>)["Idempotency-Key"],
  );
  expect(keys[0]).toBe(keys[1]);
  expect(keys[2]).not.toBe(keys[0]);
});
