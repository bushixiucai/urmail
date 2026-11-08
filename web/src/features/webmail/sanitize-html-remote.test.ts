import { describe, expect, it } from "vitest";
import { sanitizeHtmlMail } from "./sanitize-html-mail";

describe("sanitizeHtmlMail allowRemoteImages scope and hardening", () => {
  it("strips remote images by default (privacy default off)", () => {
    const out = sanitizeHtmlMail(
      '<p>x</p><img src="https://track.example/pixel.gif">',
    );
    expect(out).not.toContain("track.example");
    expect(out).toContain("img-src 'none'");
    expect(out).not.toMatch(/img-src[^;]*(https?:|data:)/);
  });

  it("keeps only absolute http and https <img src> when opted in", () => {
    const out = sanitizeHtmlMail(
      '<p>x</p><img src="https://track.example/pixel.gif"><img src="http://insecure.example/pic.jpg">',
      { allowRemoteImages: true },
    );
    expect(out).toContain("https://track.example/pixel.gif");
    expect(out).toContain("http://insecure.example/pic.jpg");
    expect(out).toContain("img-src https: http:");
  });

  it("always strips protocol-relative img src even when opted in", () => {
    const out = sanitizeHtmlMail(
      '<p>x</p><img src="//track.example/pixel.gif">',
      { allowRemoteImages: true },
    );
    expect(out).not.toContain("track.example");
  });

  it("drops an img that has only a remote or dangerous srcset and no src", () => {
    for (const allow of [false, true]) {
      const out = sanitizeHtmlMail(
        '<p>keep</p><img alt="pixel" srcset="https://track.example/only.png 1x, //track.example/proto.png 2x"><img alt="script" srcset="javascript:alert(1)">',
        { allowRemoteImages: allow },
      );
      expect(out).toContain("keep");
      expect(out).not.toContain("track.example");
      expect(out).not.toContain("srcset");
      expect(out).not.toContain("<img");
      expect(out).not.toContain("javascript:");
    }
  });

  it("always removes remote srcset even when opted in", () => {
    const out = sanitizeHtmlMail(
      '<img src="https://track.example/pixel.gif" srcset="https://track.example/pixel-2x.gif 2x">',
      { allowRemoteImages: true },
    );
    expect(out).toContain("https://track.example/pixel.gif");
    expect(out).not.toContain("pixel-2x.gif");
    expect(out).not.toContain("srcset");
  });

  it("always strips remote poster, background, and data URL attributes even when opted in", () => {
    const out = sanitizeHtmlMail(
      '<div background="https://remote.example/bg.jpg" poster="https://remote.example/poster.jpg" data="https://remote.example/obj.swf">Content</div>',
      { allowRemoteImages: true },
    );
    expect(out).not.toContain("https://remote.example/bg.jpg");
    expect(out).not.toContain("https://remote.example/poster.jpg");
    expect(out).not.toContain("https://remote.example/obj.swf");
    expect(out).not.toContain("background");
    expect(out).not.toContain("poster");
  });

  it("strips CSS url() in inline styles even when opted in", () => {
    const out = sanitizeHtmlMail(
      "<div style=\"background-image: url('https://remote.example/bg.png'); color: red;\">Styled</div>",
      { allowRemoteImages: true },
    );
    expect(out).not.toContain("https://remote.example/bg.png");
    expect(out).toContain("color: red");
  });

  it("never allows scripts, event handlers, or dangerous schemes either way", () => {
    for (const allow of [false, true]) {
      const out = sanitizeHtmlMail(
        '<script>alert(1)</script><img src="x" onerror="alert(2)"><a href="javascript:alert(3)">x</a><img src="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">',
        { allowRemoteImages: allow },
      );
      expect(out.toLowerCase()).not.toContain("<script");
      expect(out.toLowerCase()).not.toContain("onerror");
      expect(out.toLowerCase()).not.toContain("javascript:");
      expect(out.toLowerCase()).not.toContain("data:text/html");
    }
  });
});
