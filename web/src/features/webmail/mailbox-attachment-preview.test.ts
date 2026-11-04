import { afterEach, describe, expect, it, vi } from "vitest";
import {
  attachmentURL,
  boundedAttachment,
  decodePreview,
  PREVIEW_LIMIT,
  rasterDimensions,
} from "./mailbox-attachment-preview";
import { setMailboxSession } from "./mailbox-api";
const source = { folderEnc: "5bel5L2c", uid: 9, uidValidity: 42 };
const png = Uint8Array.from(
  atob(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=",
  ),
  (c) => c.charCodeAt(0),
);
afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
  localStorage.clear();
});
describe("bounded attachment previews", () => {
  it("safely previews genuine PDF bytes and handles Office documents with download guidance", () => {
    const genuinePdf = new TextEncoder().encode("%PDF-1.4 genuine content");
    const preview = decodePreview(genuinePdf, {
      part_id: "1",
      filename: "doc.pdf",
      size: genuinePdf.length,
      content_type: "application/pdf",
    });
    expect(preview.kind).toBe("pdf");
    if (preview.kind === "pdf") {
      expect(preview.dataURL).toContain("data:application/pdf;base64,");
    }

    const officePreview = decodePreview(new Uint8Array([1, 2, 3]), {
      part_id: "2",
      filename: "sheet.xlsx",
      size: 3,
      content_type:
        "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    });
    expect(officePreview.kind).toBe("office");
    if (officePreview.kind === "office") {
      expect(officePreview.filename).toBe("sheet.xlsx");
    }
  });

  it("pins folder, UID and UIDVALIDITY and refuses an unpinned source", () => {
    expect(attachmentURL(source, "1.2")).toBe(
      "/api/v1/mailbox/folders/5bel5L2c/messages/9/attachments/1.2?uid_validity=42",
    );
    expect(() => attachmentURL({ ...source, uidValidity: 0 }, "1")).toThrow();
    expect(() => attachmentURL(source, "../raw")).toThrow();
  });
  it("requires genuine bounded raster bytes, rejects active formats and pixel bombs", () => {
    expect(rasterDimensions(png, "image/png")).toEqual({ width: 1, height: 1 });
    const bomb = png.slice();
    new DataView(bomb.buffer).setUint32(16, 100000);
    expect(() => rasterDimensions(bomb, "image/png")).toThrow(
      "IMAGE_TOO_LARGE",
    );
    for (const content_type of [
      "image/svg+xml",
      "text/html",
      "application/pdf",
      "image/jpeg",
    ]) {
      expect(() =>
        decodePreview(png, {
          part_id: "2",
          filename: "x",
          size: png.length,
          content_type,
        }),
      ).toThrow();
    }
    const animated = png.slice();
    animated.set([97, 99, 84, 76], 37);
    expect(() => rasterDimensions(animated, "image/png")).toThrow();
    expect(() =>
      decodePreview(new Uint8Array([255]), {
        part_id: "1",
        filename: "x",
        size: 1,
        content_type: "text/plain",
      }),
    ).toThrow();
  });
  it("streams with the mailbox bearer and rejects an oversized chunk without buffering it", async () => {
    setMailboxSession({
      token: "test-preview-token",
      address: "a@example.test",
      expires_at: "2099-01-01",
    });
    const cancel = vi.fn();
    const fetcher = vi.fn().mockResolvedValue(
      new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new Uint8Array(PREVIEW_LIMIT + 1));
          },
          cancel,
        }),
      ),
    );
    vi.stubGlobal("fetch", fetcher);
    await expect(
      boundedAttachment(source, "1", new AbortController().signal),
    ).rejects.toThrow("PREVIEW_TOO_LARGE");
    expect(cancel).toHaveBeenCalled();
    expect(fetcher.mock.calls[0][1]).toMatchObject({
      headers: { Authorization: "Bearer test-preview-token" },
      redirect: "error",
      credentials: "omit",
    });
  });
  it("rejects stale/missing authorization rather than rendering an error body", async () => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    await expect(
      boundedAttachment(source, "1", new AbortController().signal),
    ).rejects.toThrow("AUTH_REQUIRED");
    expect(fetcher).not.toHaveBeenCalled();
    setMailboxSession({
      token: "test",
      address: "a@example.test",
      expires_at: "2099",
    });
    fetcher.mockResolvedValue(new Response("stale", { status: 409 }));
    await expect(
      boundedAttachment(source, "1", new AbortController().signal),
    ).rejects.toThrow("ATTACHMENT_UNAVAILABLE");
  });
});
