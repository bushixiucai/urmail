/**
 * Harden untrusted HTML email for iframe srcDoc rendering.
 *
 * Defense in depth alongside iframe sandbox="": strip active content,
 * neutralize dangerous URLs, drop remote images (keep cid:/data:), and
 * open links in a new tab with noopener.
 *
 * Not a full HTML sanitizer (e.g. DOMPurify); see residual gaps in tests.
 */

const STRIP_TAGS = new Set([
  "script",
  "iframe",
  "object",
  "embed",
  "applet",
  "base",
  "link",
  "form",
  "style",
  "svg",
  "source",
  "input",
  "audio",
  "video",
]);

const URL_ATTRS = new Set([
  "href",
  "src",
  "xlink:href",
  "action",
  "formaction",
  "poster",
  "data",
  "background",
]);

/** Compact and lower-case a URL for scheme checks (defeats whitespace tricks). */
function compactScheme(value: string): string {
  return (
    value
      .trim()
      // Control characters must be removed before URL scheme checks.
      // eslint-disable-next-line no-control-regex
      .replace(/[\u0000-\u001f\u007f\s]+/g, "")
      .toLowerCase()
  );
}

/** True for javascript:/vbscript: and HTML data: payloads. */
export function isDangerousUrl(value: string): boolean {
  const c = compactScheme(value);
  if (c.startsWith("javascript:") || c.startsWith("vbscript:")) return true;
  if (c.startsWith("data:text/html")) return true;
  if (c.startsWith("data:application/xhtml")) return true;
  if (c.startsWith("data:image/svg+xml")) return true;
  return false;
}

/** Remote http(s) or protocol-relative URL (tracking pixels, etc.). */
export function isRemoteUrl(value: string): boolean {
  return /^(?:https?:)?\/\//i.test(value.trim());
}

/**
 * Server-rewritten same-origin proxy URL for allowed remote images —
 * `/api/v1/mailbox/images/proxy?u=…&sig=…` (signed, opaque; the query is
 * kept verbatim, never parsed). Must be root-relative: any scheme or
 * protocol-relative host prefix means the URL is attacker-controlled,
 * not ours, and the ordinary remote-image rules apply.
 */
const IMAGE_PROXY_PATH = "/api/v1/mailbox/images/proxy";

export function isImageProxyUrl(src: string): boolean {
  const s = src.trim();
  return s === IMAGE_PROXY_PATH || s.startsWith(IMAGE_PROXY_PATH + "?");
}

/** Explicit absolute http(s) URL (rejects protocol-relative and other schemes). */
export function isAbsoluteHttpUrl(value: string): boolean {
  return /^https?:\/\//i.test(value.trim());
}

/**
 * Drop remote http(s) tracking pixels / images from HTML mail.
 * Leaves cid: and data: images alone. Removes the entire tag so the
 * renderer does not show a broken-image box.
 */
export function stripRemoteImages(html: string): string {
  return html.replace(/<img\b[^>]*>/gi, (tag) => {
    const srcMatch = /\bsrc\s*=\s*(["']?)([^"'>\s]+)\1/i.exec(tag);
    if (!srcMatch) return tag;
    const src = srcMatch[2];
    if (isRemoteUrl(src)) return "";
    return tag;
  });
}

function removeEventHandlers(el: Element): void {
  // Copy attribute names first — live NamedNodeMap mutates under removeAttribute.
  const names: string[] = [];
  for (let i = 0; i < el.attributes.length; i++) {
    names.push(el.attributes[i].name);
  }
  for (const name of names) {
    if (/^on/i.test(name)) {
      el.removeAttribute(name);
    }
  }
}

function neutralizeUrlAttrs(
  el: Element,
  allowRemote = false,
  trustedImages?: Set<string>,
): void {
  const names: string[] = [];
  for (let i = 0; i < el.attributes.length; i++) {
    names.push(el.attributes[i].name);
  }
  const isImg = el.tagName.toLowerCase() === "img";
  for (const name of names) {
    const lower = name.toLowerCase();
    if (!URL_ATTRS.has(lower) && lower !== "srcset") continue;
    const value = el.getAttribute(name);
    if (value == null) continue;
    if (lower === "srcset") {
      // Drop srcset entirely if any candidate is remote or dangerous.
      const bad = value.split(",").some((part) => {
        const url = part.trim().split(/\s+/)[0] ?? "";
        return !url || isRemoteUrl(url) || isDangerousUrl(url);
      });
      if (bad) el.removeAttribute(name);
      continue;
    }
    if (isDangerousUrl(value)) {
      el.removeAttribute(name);
      continue;
    }
    // Remote poster, background, and data are always stripped (never allowed remotely)
    if (
      (lower === "poster" || lower === "background" || lower === "data") &&
      isRemoteUrl(value)
    ) {
      el.removeAttribute(name);
      continue;
    }
    // src on non-img elements is always stripped
    if (lower === "src" && !isImg) {
      el.removeAttribute(name);
      continue;
    }
    // src on img elements: only allowed if trusted, image proxy, or opted-in absolute http(s)
    if (lower === "src" && isImg) {
      const s = value.trim();
      const ok =
        (trustedImages && trustedImages.has(s)) ||
        isImageProxyUrl(s) ||
        (allowRemote && isAbsoluteHttpUrl(s));
      if (!ok) {
        el.removeAttribute(name);
      }
    }
  }
}

function hardenAnchor(el: Element): void {
  if (el.tagName.toLowerCase() !== "a") return;
  const href = el.getAttribute("href");
  if (href != null && isDangerousUrl(href)) {
    el.removeAttribute("href");
  }
  el.setAttribute("rel", "noopener noreferrer");
  el.setAttribute("target", "_blank");
}

function shouldStripImg(
  el: Element,
  allowRemote = false,
  trustedImages?: Set<string>,
): boolean {
  const src = el.getAttribute("src");
  if (src != null) {
    const s = src.trim();
    if (isDangerousUrl(s)) return true;
    if (trustedImages && trustedImages.has(s)) return false;
    if (isImageProxyUrl(s)) return false;
    if (allowRemote && isAbsoluteHttpUrl(s)) return false;
    // Any other src (root-relative non-proxy, relative, protocol-relative,
    // unallowed remote, un-inlined cid/data/blob, etc.) is stripped.
    return true;
  }
  const srcset = el.getAttribute("srcset");
  if (srcset) {
    const remote = srcset.split(",").some((part) => {
      const url = part.trim().split(/\s+/)[0] ?? "";
      return !url || isRemoteUrl(url) || isDangerousUrl(url);
    });
    if (remote) return true;
  }
  // An img with no src and no srcset is inert (e.g. placeholder/data-blocked-src).
  return false;
}

function isMetaRefresh(el: Element): boolean {
  if (el.tagName.toLowerCase() !== "meta") return false;
  const equiv = el.getAttribute("http-equiv");
  return equiv != null && equiv.trim().toLowerCase() === "refresh";
}

/** True when the document already carries a CSP meta (server-injected). */
function hasCspMeta(doc: Document): boolean {
  for (const meta of Array.from(doc.querySelectorAll("meta"))) {
    const equiv = meta.getAttribute("http-equiv");
    if (equiv && equiv.trim().toLowerCase() === "content-security-policy") {
      return true;
    }
  }
  return false;
}

// Only CSS properties that cannot fetch or decode image resources are kept.
// CSSStyleDeclaration resolves property escapes before this allowlist is read.
const SAFE_STYLES = new Set([
  "color",
  // Dark-themed mail paints white text on a dark canvas; stripping the
  // background collapses it into white-on-white (prod: Infura mail).
  // Plain colors carry no fetch/decode surface — the value filter below
  // still rejects url()/var()/escapes.
  "background-color",
  "font-family",
  "font-size",
  "font-style",
  "font-weight",
  "font-variant",
  "line-height",
  "letter-spacing",
  "word-spacing",
  "text-align",
  "text-decoration",
  "text-transform",
  "text-indent",
  "white-space",
  "word-break",
  "overflow-wrap",
  "vertical-align",
  "display",
  "width",
  "height",
  "min-width",
  "max-width",
  "min-height",
  "max-height",
  "margin",
  "margin-top",
  "margin-right",
  "margin-bottom",
  "margin-left",
  "padding",
  "padding-top",
  "padding-right",
  "padding-bottom",
  "padding-left",
  "border",
  "border-width",
  "border-style",
  "border-color",
  "border-collapse",
  "border-spacing",
  "border-top",
  "border-right",
  "border-bottom",
  "border-left",
  "border-radius",
  "table-layout",
  "direction",
  "unicode-bidi",
]);
function sanitizeStyles(el: Element): void {
  const declaration = (el as HTMLElement).style;
  if (!declaration) {
    el.removeAttribute("style");
    return;
  }
  // `background:#121212` (shorthand, ubiquitous in MJML mail) enumerates as
  // "background" which is not allowlisted — but its color component is safe
  // and CSSOM exposes it as background-color. Recover that color before the
  // shorthand is dropped, or dark canvases collapse to white-on-white.
  const bgColor = declaration.getPropertyValue("background-color");
  const names = Array.from(declaration);
  for (const property of names) {
    const value = declaration.getPropertyValue(property);
    if (
      !SAFE_STYLES.has(property.toLowerCase()) ||
      /(?:url|var)\s*\(|\\/i.test(value)
    )
      declaration.removeProperty(property);
  }
  if (
    bgColor &&
    !/(?:url|var)\s*\(|\\/i.test(bgColor) &&
    !Array.from(declaration).some((p) => p.toLowerCase() === "background-color")
  ) {
    // The color came solely from the shorthand (it enumerates as
    // "background", which the allowlist drops) — restore it as a longhand.
    declaration.setProperty("background-color", bgColor);
  }
  if (!declaration.length) el.removeAttribute("style");
  el.removeAttribute("background");
  el.removeAttribute("poster");
  el.removeAttribute("srcset");
}

function sanitizeElement(
  el: Element,
  allowRemote = false,
  trustedImages?: Set<string>,
): void {
  const tag = el.tagName.toLowerCase();

  if (STRIP_TAGS.has(tag) || isMetaRefresh(el)) {
    el.remove();
    return;
  }

  if (tag === "img" && shouldStripImg(el, allowRemote, trustedImages)) {
    el.remove();
    return;
  }

  sanitizeStyles(el);
  removeEventHandlers(el);
  neutralizeUrlAttrs(el, allowRemote, trustedImages);
  hardenAnchor(el);

  // Child list mutates as we remove nodes — snapshot first.
  const children = Array.from(el.children);
  for (const child of children) {
    sanitizeElement(child, allowRemote, trustedImages);
  }
}

/**
 * Sanitize HTML email for safe display in a sandboxed iframe via srcDoc.
 * Preserve safe inline typography; discard stylesheets and image-bearing CSS.
 */
export function sanitizeHtmlMail(
  html: string,
  opts?: { allowRemoteImages?: boolean; inlineImages?: Record<string, string> },
): string {
  if (!html) return "";
  const allowRemote = opts?.allowRemoteImages === true;
  const doc = new DOMParser().parseFromString(html, "text/html");
  const trustedImages = new Set<string>();
  for (const image of doc.querySelectorAll("img")) {
    const src = image.getAttribute("src") ?? "";
    const inline = opts?.inlineImages?.[src];
    if (
      inline &&
      /^data:image\/(png|jpeg);base64,[A-Za-z0-9+/=]+$/.test(inline)
    ) {
      trustedImages.add(inline);
      image.setAttribute("src", inline);
      image.removeAttribute("srcset");
    } else if (
      /^(?:cid|data|blob):/i.test(src.trim()) ||
      (src.startsWith("/api/v1/mailbox/") && !isImageProxyUrl(src))
    ) {
      image.remove();
    }
  }
  if (doc.documentElement) {
    // NOTE: comment nodes (incl. MSO conditional comments, often carrying
    // <style> blocks in real mail) are intentionally left in place —
    // dropping them in jsdom swallows content that the HTML parser scopes
    // between conditional-comment fences (observed: a bgcolor'd table
    // inside `<!--[if !mso]><!-->` vanished). Sandbox+CSP keeps any such
    // residue inert for modern engines.
    sanitizeElement(doc.documentElement, allowRemote, trustedImages);
  }
  // This policy is independent of the host page: CSS backgrounds, imports,
  // fonts, SVG references and media cannot contact remote origins. When the
  // server already wrapped the body with its own CSP meta (single source of
  // truth), do not inject a second policy — but the sanitizer itself still
  // runs as defense in depth.
  if (!hasCspMeta(doc)) {
    const hasProxyImages = Array.from(doc.querySelectorAll("img")).some((img) =>
      isImageProxyUrl(img.getAttribute("src") ?? ""),
    );
    const imgSrcParts: string[] = [];
    if (hasProxyImages) imgSrcParts.push("'self'");
    if (allowRemote) imgSrcParts.push("https:", "http:");
    if (trustedImages.size) imgSrcParts.push("data:");
    const imgSrc = imgSrcParts.length > 0 ? imgSrcParts.join(" ") : "'none'";

    const policy = doc.createElement("meta");
    policy.httpEquiv = "Content-Security-Policy";
    policy.content =
      "default-src 'none'; img-src " +
      imgSrc +
      "; style-src 'unsafe-inline'; font-src 'none'; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; media-src 'none'; object-src 'none'; script-src 'none'";
    doc.head.prepend(policy);
  }
  return "<!doctype html>" + (doc.documentElement?.outerHTML ?? "");
}
