import { describe, expect, it } from "vitest";
import { sanitizeHtmlMail } from "./sanitize-html-mail";

describe("sanitizeHtmlMail allowRemoteImages", () => {
  it("strips remote images by default (privacy default off)", () => {
    const out = sanitizeHtmlMail(
      '<p>x</p><img src="https://track.example/pixel.gif">',
    );
    expect(out).not.toContain("track.example");
  });

  it("keeps remote images when the user opted in, and allows them in CSP", () => {
    const out = sanitizeHtmlMail(
      '<p>x</p><img src="https://track.example/pixel.gif">',
      { allowRemoteImages: true },
    );
    expect(out).toContain("https://track.example/pixel.gif");
    expect(out).toContain("img-src https: http:");
  });

  it("never allows scripts or javascript: URLs either way", () => {
    for (const allow of [false, true]) {
      const out = sanitizeHtmlMail(
        '<script>alert(1)</script><a href="javascript:alert(1)">x</a>',
        { allowRemoteImages: allow },
      );
      expect(out.toLowerCase()).not.toContain("<script");
      expect(out.toLowerCase()).not.toContain("javascript:");
    }
  });
});
