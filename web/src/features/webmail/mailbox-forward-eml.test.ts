import { afterEach, expect, it, vi } from "vitest";
import {
  fetchMessageAsOutgoing,
  MAX_SEND_ATTACHMENT_BYTES,
} from "./mailbox-api";
afterEach(() => {
  sessionStorage.clear();
  vi.unstubAllGlobals();
});
it("encodes exact original MIME bytes as one rfc822 attachment with source generation", async () => {
  const raw = new Uint8Array([
    70, 114, 111, 109, 58, 32, 97, 13, 10, 13, 10, 0, 255,
  ]);
  const fetcher = vi.fn(
    async (_url: string, _options?: RequestInit) =>
      new Response(raw, { status: 200 }),
  );
  vi.stubGlobal("fetch", fetcher);
  const result = await fetchMessageAsOutgoing("b64.SU5CT1g", 19, 123);
  expect(result.content_type).toBe("message/rfc822");
  expect(result.filename).toBe("message-19.eml");
  expect(
    Uint8Array.from(atob(result.content_base64), (c) => c.charCodeAt(0)),
  ).toEqual(raw);
  expect(fetcher.mock.calls[0][0]).toContain("/19/raw?uid_validity=123");
});
it("cancels an oversized original stream before producing a partial EML", async () => {
  const cancelled = vi.fn();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new Uint8Array(MAX_SEND_ATTACHMENT_BYTES + 1));
    },
    cancel: cancelled,
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(stream)),
  );
  await expect(fetchMessageAsOutgoing("INBOX", 19, 123)).rejects.toThrow(
    "ATTACHMENT_TOO_LARGE",
  );
  expect(cancelled).toHaveBeenCalled();
});
