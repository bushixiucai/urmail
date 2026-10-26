import { describe, expect, it } from "vitest";
import {
  isDangerousUrl,
  isRemoteUrl,
  sanitizeHtmlMail,
  stripRemoteImages,
} from "./sanitize-html-mail";

function bodyHtml(sanitized: string): string {
  const doc = new DOMParser().parseFromString(sanitized, "text/html");
  return doc.body.innerHTML;
}

describe("isDangerousUrl / isRemoteUrl", () => {
  it("flags javascript and whitespace tricks", () => {
    expect(isDangerousUrl("javascript:alert(1)")).toBe(true);
    expect(isDangerousUrl(" JavaScript:alert(1)")).toBe(true);
    expect(isDangerousUrl("java\nscript:alert(1)")).toBe(true);
    expect(isDangerousUrl("vbscript:msgbox(1)")).toBe(true);
  });

  it("flags HTML data URLs but not image data URLs", () => {
    expect(isDangerousUrl("data:text/html,<script>")).toBe(true);
    expect(isDangerousUrl("data:image/png;base64,abc")).toBe(false);
  });

  it("detects remote and protocol-relative URLs", () => {
    expect(isRemoteUrl("https://evil.test/p.png")).toBe(true);
    expect(isRemoteUrl("http://evil.test/p.png")).toBe(true);
    expect(isRemoteUrl("//evil.test/p.png")).toBe(true);
    expect(isRemoteUrl("cid:part1")).toBe(false);
    expect(isRemoteUrl("data:image/png;base64,aa")).toBe(false);
  });
});

describe("stripRemoteImages", () => {
  it("removes remote http(s) and protocol-relative img tags", () => {
    expect(
      stripRemoteImages('<p><img src="https://t.example/px.gif">ok</p>'),
    ).toBe("<p>ok</p>");
    expect(stripRemoteImages('<img src="//cdn.example/a.png">')).toBe("");
    expect(stripRemoteImages('<img src="http://x/y.jpg">')).toBe("");
  });

  it("keeps cid: and data: images", () => {
    const cid = '<img src="cid:logo@mail">';
    const data = '<img src="data:image/png;base64,abc">';
    expect(stripRemoteImages(cid)).toBe(cid);
    expect(stripRemoteImages(data)).toBe(data);
  });
});

describe("sanitizeHtmlMail", () => {
  it("strips script tags and content", () => {
    const out = sanitizeHtmlMail(
      "<p>hi</p><script>alert(1)</script><p>bye</p>",
    );
    expect(bodyHtml(out)).not.toMatch(/script/i);
    expect(bodyHtml(out)).toContain("hi");
    expect(bodyHtml(out)).toContain("bye");
  });

  it("removes event handler attributes", () => {
    const out = sanitizeHtmlMail(
      '<p onclick="alert(1)" onmouseover="evil()">text</p>',
    );
    const html = bodyHtml(out);
    expect(html).not.toMatch(/onclick/i);
    expect(html).not.toMatch(/onmouseover/i);
    expect(html).toContain("text");
  });

  it("neutralizes javascript: URLs on anchors", () => {
    const out = sanitizeHtmlMail('<a href="javascript:alert(1)">click</a>');
    const doc = new DOMParser().parseFromString(out, "text/html");
    const a = doc.querySelector("a");
    expect(a).not.toBeNull();
    expect(a!.getAttribute("href")).toBeNull();
    expect(a!.getAttribute("rel")).toBe("noopener noreferrer");
    expect(a!.getAttribute("target")).toBe("_blank");
  });

  it("rewrites safe anchors with noopener noreferrer and _blank", () => {
    const out = sanitizeHtmlMail('<a href="https://example.com/path">go</a>');
    const doc = new DOMParser().parseFromString(out, "text/html");
    const a = doc.querySelector("a");
    expect(a!.getAttribute("href")).toBe("https://example.com/path");
    expect(a!.getAttribute("rel")).toBe("noopener noreferrer");
    expect(a!.getAttribute("target")).toBe("_blank");
  });

  it("strips iframe, object, and embed", () => {
    const out = sanitizeHtmlMail(
      '<p>x</p><iframe src="https://evil"></iframe><object data="x"></object><embed src="y">',
    );
    const html = bodyHtml(out);
    expect(html).not.toMatch(/iframe|object|embed/i);
    expect(html).toContain("x");
  });

  it("strips meta refresh", () => {
    const out = sanitizeHtmlMail(
      '<html><head><meta http-equiv="refresh" content="0;url=https://evil"></head><body>ok</body></html>',
    );
    expect(out).not.toMatch(/refresh/i);
    expect(out).toContain("ok");
  });

  it("keeps cid: and data: images while removing remote ones", () => {
    const out = sanitizeHtmlMail(
      [
        '<img src="cid:part1">',
        '<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=">',
        '<img src="https://tracker.example/pixel.gif">',
        '<img src="//cdn.example/x.png">',
      ].join(""),
    );
    const doc = new DOMParser().parseFromString(out, "text/html");
    const imgs = [...doc.querySelectorAll("img")];
    expect(imgs).toHaveLength(2);
    expect(imgs[0].getAttribute("src")).toBe("cid:part1");
    expect(imgs[1].getAttribute("src")).toMatch(/^data:image\//);
  });

  it("preserves head styles for full HTML documents", () => {
    const out = sanitizeHtmlMail(
      '<html><head><style>.x{color:red}</style></head><body><p class="x">hi</p></body></html>',
    );
    expect(out).toMatch(/\.x\{color:red\}/);
    expect(out).toContain("hi");
  });

  it("does not alter plain-text callers (empty / non-HTML passthrough shape)", () => {
    expect(sanitizeHtmlMail("")).toBe("");
    // Fragment text still wraps in a document; body text preserved.
    const out = sanitizeHtmlMail("Just plain words &amp; symbols");
    expect(bodyHtml(out)).toContain("Just plain words");
  });
});

describe("HTML mail network policy", () => {
  it("places an independent restrictive CSP before attacker styles and policies", () => {
    const out = sanitizeHtmlMail(
      "<head><style>@import url(https://tracker.test/a.css);p{background:url(https://tracker.test/pixel)}</style></head><body><p>readable</p></body>",
    );
    const doc = new DOMParser().parseFromString(out, "text/html");
    const policy = doc.head.firstElementChild;
    expect(policy?.getAttribute("http-equiv")).toBe("Content-Security-Policy");
    const csp = policy?.getAttribute("content") ?? "";
    expect(csp).toContain("default-src 'none'");
    expect(csp).toContain("img-src data: cid:");
    expect(csp).toContain("style-src 'unsafe-inline'");
    expect(csp).toContain("font-src 'none'");
    expect(csp).toContain("connect-src 'none'");
    expect(doc.body.textContent).toContain("readable");
  });
});
