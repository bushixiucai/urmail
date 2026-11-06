import { describe, it, expect, afterEach } from "vitest";
import { sanitizeHtmlMail } from "./sanitize-html-mail";
import {
  resetSanitizeWorkerForTests,
  sanitizeHtmlMailAsync,
  sanitizeWorkerAvailable,
} from "./sanitize-html-mail-async";

type FakeResponse = { id: number; html?: string; error?: string };

class FakeWorker {
  onmessage: ((event: MessageEvent<FakeResponse>) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  terminated = false;
  post: (data: { id: number; html: string }) => void = () => {};

  postMessage(data: { id: number; html: string }) {
    this.post(data);
  }

  terminate() {
    this.terminated = true;
  }
}

afterEach(() => {
  resetSanitizeWorkerForTests(null);
});

describe("sanitizeHtmlMailAsync", () => {
  it("uses the sync sanitizer when Worker is unavailable", async () => {
    expect(sanitizeWorkerAvailable()).toBe(false);
    const html = "<p>Hello</p><script>alert(1)</script>";
    const out = await sanitizeHtmlMailAsync(html);
    expect(out).toBe(sanitizeHtmlMail(html));
    expect(out).not.toContain("<script");
    expect(out).toContain("Hello");
  });

  it("returns the worker payload and does not sanitize on the caller", async () => {
    const worker = new FakeWorker();
    worker.post = (data) => {
      setTimeout(() => {
        worker.onmessage?.({
          data: { id: data.id, html: "<p>from-worker</p>" },
        } as MessageEvent<FakeResponse>);
      }, 0);
    };
    resetSanitizeWorkerForTests(() => worker);
    expect(sanitizeWorkerAvailable()).toBe(true);

    const pending = sanitizeHtmlMailAsync(
      "<p>raw</p><script>alert(1)</script>",
    );
    const out = await pending;
    expect(out).toBe("<p>from-worker</p>");
    expect(out).not.toContain("alert(1)");
  });

  it("falls back to the sync sanitizer when the worker errors", async () => {
    const worker = new FakeWorker();
    worker.post = () => {
      setTimeout(() => {
        worker.onerror?.(new Event("error"));
      }, 0);
    };
    resetSanitizeWorkerForTests(() => worker);
    const html = "<p>ok</p><script>x</script>";
    await expect(sanitizeHtmlMailAsync(html)).resolves.toBe(
      sanitizeHtmlMail(html),
    );
  });
});
