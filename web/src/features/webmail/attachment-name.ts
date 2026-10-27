/**
 * Safe attachment filename display (WEBMAIL-007).
 *
 * Server filenames are untrusted MIME parameters: strip path traversal and
 * separators, drop control characters, decode a leftover RFC 2047 encoded
 * word, and cap the length. The display value is never used for filesystem
 * access (downloads set their own client-side name), so this is purely a
 * presentation guard.
 */

const MAX_NAME_LENGTH = 200;

/** Decode a single RFC 2047 encoded-word; returns input when not encoded. */
export function decodeRfc2047(value: string): string {
  const match = /=\?([^?]+)\?([bBqQ])\?([^?]*)\?=/.exec(value);
  if (!match) return value;
  const [, charset, enc, text] = match;
  try {
    if (enc.toLowerCase() === "b") {
      const bytes = Uint8Array.from(atob(text), (c) => c.charCodeAt(0));
      const label = charset.toLowerCase() === "gbk" ? "gb18030" : charset;
      return new TextDecoder(label).decode(bytes);
    }
    // Quoted-printable Q encoding.
    const bytes: number[] = [];
    for (let i = 0; i < text.length; i++) {
      const ch = text[i];
      if (ch === "=") {
        bytes.push(parseInt(text.slice(i + 1, i + 3), 16));
        i += 2;
      } else if (ch === "_") {
        bytes.push(0x20);
      } else {
        bytes.push(ch.charCodeAt(0));
      }
    }
    return new TextDecoder(charset.toLowerCase()).decode(
      Uint8Array.from(bytes),
    );
  } catch {
    return value;
  }
}

/**
 * Human-safe display name for an attachment. Falls back to the part ID when
 * nothing sensible remains.
 */
export function attachmentDisplayName(
  filename: string,
  partID: string,
): string {
  let name = (filename || "").trim();
  if (/=?[^?]+?[bBqQ]?/.test(name)) name = decodeRfc2047(name);
  // Flatten any path component (unix + windows separators, traversal).
  name = name.split(/[/\\]+/).pop() ?? "";
  if (name === "." || name === "..") name = "";
  // eslint-disable-next-line no-control-regex
  name = name.replace(/[\u0000-\u001f\u007f]/g, "");
  name = name.trim();
  if (name.length > MAX_NAME_LENGTH) {
    name = name.slice(0, MAX_NAME_LENGTH);
  }
  return name || "attachment-" + partID;
}
