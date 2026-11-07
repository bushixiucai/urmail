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

function neutralizeUrlAttrs(el: Element, allowRemote = false): void {
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
    // Remote src on non-img elements is always stripped
    if (lower === "src" && !isImg && isRemoteUrl(value)) {
      el.removeAttribute(name);
      continue;
    }
    // Remote src on img elements: only allowed if allowRemote is true AND it is an absolute http(s) URL
    if (lower === "src" && isImg && isRemoteUrl(value)) {
      if (!allowRemote || !isAbsoluteHttpUrl(value)) {
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

function shouldStripImg(el: Element, allowRemote = false): boolean {
  const src = el.getAttribute("src");
  if (src != null && isDangerousUrl(src)) return true;
  if (src != null && isRemoteUrl(src)) {
    if (!allowRemote || !isAbsoluteHttpUrl(src)) return true;
  }
  if (src == null) {
    const srcset = el.getAttribute("srcset");
    if (srcset) {
      const remote = srcset.split(",").some((part) => {
        const url = part.trim().split(/\s+/)[0] ?? "";
        return url && (isRemoteUrl(url) || isDangerousUrl(url));
      });
      if (remote) return true;
    }
  }
  return false;
}

function isMetaRefresh(el: Element): boolean {
  if (el.tagName.toLowerCase() !== "meta") return false;
  const equiv = el.getAttribute("http-equiv");
  return equiv != null && equiv.trim().toLowerCase() === "refresh";
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
  const names = Array.from(declaration);
  for (const property of names) {
    const value = declaration.getPropertyValue(property);
    if (
      !SAFE_STYLES.has(property.toLowerCase()) ||
      /(?:url|var)\s*\(|\\/i.test(value)
    )
      declaration.removeProperty(property);
  }
  if (!declaration.length) el.removeAttribute("style");
  el.removeAttribute("background");
  el.removeAttribute("poster");
  el.removeAttribute("srcset");
}

function sanitizeElement(el: Element, allowRemote = false): void {
  const tag = el.tagName.toLowerCase();

  if (STRIP_TAGS.has(tag) || isMetaRefresh(el)) {
    el.remove();
    return;
  }

  if (tag === "img" && shouldStripImg(el, allowRemote)) {
    el.remove();
    return;
  }

  sanitizeStyles(el);
  removeEventHandlers(el);
  neutralizeUrlAttrs(el, allowRemote);
  hardenAnchor(el);

  // Child list mutates as we remove nodes — snapshot first.
  const children = Array.from(el.children);
  for (const child of children) {
    sanitizeElement(child, allowRemote);
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
      src.startsWith("/api/v1/mailbox/")
    ) {
      image.remove();
    }
  }
  if (doc.documentElement) {
    sanitizeElement(doc.documentElement, allowRemote);
  }
  // This policy is independent of the host page: CSS backgrounds, imports,
  // fonts, SVG references and media cannot contact remote origins.
  const policy = doc.createElement("meta");
  policy.httpEquiv = "Content-Security-Policy";
  policy.content =
    "default-src 'none'; img-src " +
    (allowRemote ? "https: http: " : "") +
    (trustedImages.size ? "data:" : allowRemote ? "" : "'none'") +
    "; style-src 'unsafe-inline'; font-src 'none'; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; media-src 'none'; object-src 'none'";
  doc.head.prepend(policy);
  return "<!doctype html>" + (doc.documentElement?.outerHTML ?? "");
}
