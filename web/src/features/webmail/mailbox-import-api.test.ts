import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "@/lib/api/client";
import {
  downloadMailboxMbox,
  MAX_MAIL_IMPORT_BYTES,
} from "./mailbox-import-api";

beforeEach(() => {
  vi.useFakeTimers();
  sessionStorage.setItem("urmail.mailbox-token", "fixture-mailbox-token");
});
afterEach(() => {
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  sessionStorage.clear();
});
describe("bounded native MBOX export", () => {
  it("binds the mailbox bearer, selected UIDs and UIDVALIDITY", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response("From fixture@example.test now\nSubject: test\n\nbody\n", {
        headers: { "Content-Type": "application/mbox" },
      }),
    );
    vi.stubGlobal("fetch", fetch);
    const objectURL = vi.fn().mockReturnValue("blob:owned-export");
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = objectURL;
        static revokeObjectURL = vi.fn();
      },
    );
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    await downloadMailboxMbox("b64.SU5CT1g", 89, [1, 7]);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/mailbox/folders/b64.SU5CT1g/export.mbox?uid_validity=89&uids=1%2C7",
      expect.objectContaining({
        headers: { Authorization: "Bearer fixture-mailbox-token" },
        redirect: "error",
        cache: "no-store",
      }),
    );
    expect(objectURL).toHaveBeenCalledTimes(1);
  });
  it("cancels an oversized stream without creating a download", async () => {
    const cancel = vi.fn();
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(MAX_MAIL_IMPORT_BYTES + 1));
      },
      cancel,
    });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(stream, {
          headers: { "Content-Type": "application/mbox" },
        }),
      ),
    );
    await expect(
      downloadMailboxMbox("b64.SU5CT1g", 9, [1]),
    ).rejects.toMatchObject({
      code: "MAIL_IMPORT_EXPORT_TOO_LARGE",
      status: 413,
    });
    expect(cancel).toHaveBeenCalledTimes(1);
  });
  it("does not download a successful HTML fallback as mail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("<html>application</html>", {
          headers: { "Content-Type": "text/html" },
        }),
      ),
    );
    await expect(
      downloadMailboxMbox("b64.SU5CT1g", 9, [1]),
    ).rejects.not.toBeInstanceOf(ApiRequestError);
  });
});
