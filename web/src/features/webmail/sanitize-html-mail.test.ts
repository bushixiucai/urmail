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

  it("drops sender data images, unresolved CID and remote images", () => {
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
    expect(imgs).toHaveLength(0);
  });

  it("removes untrusted stylesheets while preserving message text", () => {
    const out = sanitizeHtmlMail(
      '<html><head><style>.x{color:red}</style></head><body><p class="x">hi</p></body></html>',
    );
    expect(out).not.toContain("<style");
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
    expect(csp).toContain("img-src 'none'");
    expect(csp).toContain("style-src 'unsafe-inline'");
    expect(csp).toContain("font-src 'none'");
    expect(csp).toContain("connect-src 'none'");
    expect(doc.body.textContent).toContain("readable");
  });
});

it("renders only verified CID mappings and drops protected URLs without bearer access", () => {
  const html =
    '<img src="cid:logo"><img src="/api/v1/mailbox/folders/x/messages/1/attachments/2"><img src="cid:missing">';
  const result = sanitizeHtmlMail(html, {
    inlineImages: {
      "cid:logo": "data:image/png;base64,aGVsbG8=",
    },
  });
  expect(result).toContain('src="data:image/png;base64,aGVsbG8="');
  expect(result).not.toContain('src="cid:');
  expect(result).not.toContain('src="/api/v1/mailbox/');
  expect(result).toContain("connect-src 'none'");
});

it("keeps paired foreground/background colors so dark-themed mail stays legible", () => {
  // Dark marketing mail: white text over a dark canvas. Stripping
  // background-color collapses it into white-on-white (prod: Infura mail).
  const darkMail =
    '<body style="background-color:#0b0b0f;color:#ffffff"><div style="background-color:#1a1a22;color:#f5f5f5">Weekly stats</div><table><td bgcolor="#101018" style="color:#e5e5e5">Cell copy</td></table></body>';
  const out = sanitizeHtmlMail(darkMail);
  expect(out).toContain("background-color:#0b0b0f");
  expect(out).toMatch(/background-color:\s*#1a1a22/i);
  expect(out).toMatch(/color:\s*#ffffff/i);
  // The HTML bgcolor attribute survives for table-driven layouts.
  expect(out).toContain('bgcolor="#101018"');
});

it("still strips background-color payloads that try to fetch resources", () => {
  const hostile =
    '<div style="background-color: url(https://tracker.test/x.png)">p</div><div style="background-color:expression(alert(1))">q</div>';
  const out = sanitizeHtmlMail(hostile);
  expect(out).not.toContain("tracker.test");
  expect(out).not.toContain("expression");
});

it("recovers the color from a background shorthand with no explicit longhand", () => {
  // Real MJML mail (prod Infura) paints dark canvases with the shorthand
  // `background:#121212` — it enumerates as "background", which the
  // allowlist drops, so the color must be recovered from CSSOM before the
  // shorthand goes or dark mail collapses to white-on-white even with
  // background-color allowlisted.
  const out = sanitizeHtmlMail(
    '<body style="background:#121212;color:#ffffff"><p>Weekly stats</p></body>',
  );
  expect(out).toMatch(/background-color:\s*(#121212|rgb\(18,\s*18,\s*18\))/i);
  expect(out).toMatch(/color:\s*(#ffffff|rgb\(255,\s*255,\s*255\))/i);
});

it("keeps the longhand when a background shorthand sits beside it (MJML pattern)", () => {
  // MJML emits `background:#fff;background-color:#fff` on the same wrapper;
  // the shorthand removal must not take the allowlisted longhand with it.
  const out = sanitizeHtmlMail(
    '<div style="background:#ffffff;background-color:#ffffff;margin:0px auto;max-width:550px"><p>Body</p></div>',
  );
  expect(out).toMatch(
    /background-color:\s*(#ffffff|rgb\(255,\s*255,\s*255\))/i,
  );
  // The shorthand itself is dropped (no raw `background:` stays behind) and
  // a fetch-bearing neighbor is still stripped.
  expect(out).not.toMatch(/background:/i);
  const hostileNeighbor = sanitizeHtmlMail(
    '<div style="background:#fff;background-image:url(https://t.test/x.png)"><p>B</p></div>',
  );
  expect(hostileNeighbor).not.toContain("t.test");
  expect(hostileNeighbor).not.toMatch(/background-image/i);
});

it("leaves MSO conditional comments (and any inline residue) untouched", () => {
  // Real mail fences Outlook-only blocks in conditional comments that may
  // contain <style> markup; dropping comment nodes swallows visible content
  // scoped between the fences (observed with a bgcolor'd table). The
  // sandbox+CSP iframe keeps such residue inert for modern engines.
  const mail =
    '<!--[if mso]><style>p{width:100% !important}</style><![endif]--><table><td bgcolor="#101018">Cell</td></table>';
  const out = sanitizeHtmlMail(mail);
  expect(out).toContain('bgcolor="#101018"');
  expect(out).toContain("Cell");
});
