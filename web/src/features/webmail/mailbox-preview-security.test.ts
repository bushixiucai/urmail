import { expect, it } from "vitest";
import { sanitizeHtmlMail } from "./sanitize-html-mail";
import { rasterDimensions } from "./mailbox-attachment-preview";
it("does not grant data image decoding through markup, srcset or CSS", () => {
  const html = sanitizeHtmlMail(
    '<img src="data:image/png;base64,YQ=="><img src="blob:https://mail.test/untrusted"><style>.bomb{background:url(data:image/gif;base64,YQ==)}</style><p style="background-image:url(data:image/png;base64,YQ==)">body</p><img srcset="data:image/png;base64,YQ==">',
  );
  const doc = new DOMParser().parseFromString(html, "text/html");
  expect(doc.querySelectorAll("img[src]")).toHaveLength(0);
  const policy = doc.querySelector("meta")!.content;
  expect(policy).toContain("img-src 'none'");
  expect(policy).not.toMatch(/img-src[^;]*(https?:|data:)/);
});
it("allows only authorized raster objects without enabling sender data URLs", () => {
  const doc = new DOMParser().parseFromString(
    sanitizeHtmlMail(
      '<img src="cid:verified"><img src="data:image/png;base64,YQ==">',
      { inlineImages: { "cid:verified": "data:image/png;base64,YQ==" } },
    ),
    "text/html",
  );
  expect(doc.querySelectorAll("img")).toHaveLength(1);
  expect(doc.querySelector("img")!.src).toBe("data:image/png;base64,YQ==");
  expect(doc.querySelector("meta")!.content).toContain("img-src data:");
  expect(doc.querySelector("style")).toBeNull();
});
it("rejects duplicate PNG headers and animation chunks", () => {
  const original = Uint8Array.from(
    atob(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=",
    ),
    (c) => c.charCodeAt(0),
  );
  const duplicate = new Uint8Array(original.length + 25);
  duplicate.set(original.subarray(0, 33));
  duplicate.set(original.subarray(8, 33), 33);
  duplicate.set(original.subarray(33), 58);
  expect(() => rasterDimensions(duplicate, "image/png")).toThrow(
    "INVALID_IMAGE",
  );
  for (const marker of [0x6163544c, 0x6663544c, 0x66644154]) {
    const animated = original.slice();
    new DataView(animated.buffer).setUint32(37, marker);
    expect(() => rasterDimensions(animated, "image/png")).toThrow(
      "INVALID_IMAGE",
    );
  }
});
it("rejects a second JPEG frame even after entropy begins", () => {
  const frame = [255, 192, 0, 8, 8, 0, 1, 0, 1, 1];
  expect(
    rasterDimensions(
      new Uint8Array([255, 216, ...frame, 255, 217]),
      "image/jpeg",
    ),
  ).toEqual({ width: 1, height: 1 });
  expect(() =>
    rasterDimensions(
      new Uint8Array([
        255,
        216,
        ...frame,
        255,
        218,
        0,
        2,
        0,
        ...frame,
        255,
        217,
      ]),
      "image/jpeg",
    ),
  ).toThrow("INVALID_IMAGE");
});
it("removes CSS and alternate-image decoding paths even with a trusted CID image", () => {
  const doc = new DOMParser().parseFromString(
    sanitizeHtmlMail(
      '<style>.x{background:url(data:image/png;base64,YQ==)}</style><picture><source srcset="data:image/png;base64,YQ=="><img src="cid:verified" srcset="data:image/png;base64,YQ=="></picture><svg><image href="data:image/png;base64,YQ=="/></svg><p style="color:red;font-size:14px;--bomb:url(data:image/png;base64,YQ==);background-image:var(--bomb);filter:url(data:image/svg+xml,x);cursor:url(data:image/png;base64,YQ==),auto" background="data:image/png;base64,YQ==">text</p>',
      { inlineImages: { "cid:verified": "data:image/png;base64,YQ==" } },
    ),
    "text/html",
  );
  expect(
    doc.querySelectorAll("style,source,svg,[srcset],[background]"),
  ).toHaveLength(0);
  const paragraph = doc.querySelector("p")!;
  expect(paragraph.style.color).toBe("red");
  expect(paragraph.style.fontSize).toBe("14px");
  expect(paragraph.getAttribute("style")).not.toMatch(
    /url|var|--bomb|background|filter|cursor/,
  );
});
