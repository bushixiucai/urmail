import { getMailboxToken, type AttachmentMeta } from "./mailbox-api";

export interface AttachmentSource {
  folderEnc: string;
  uid: number;
  uidValidity: number;
}
export const PREVIEW_LIMIT = 1024 * 1024;
export const PREVIEW_PIXELS = 16_000_000;
const TEXT_TYPES = new Set(["text/plain", "text/csv", "application/json"]);
export function previewType(type: string): "image" | "text" | null {
  const mime = type.split(";", 1)[0].trim().toLowerCase();
  if (mime === "image/png" || mime === "image/jpeg") return "image";
  return TEXT_TYPES.has(mime) ? "text" : null;
}
export function attachmentURL(source: AttachmentSource, part: string): string {
  if (
    !source.folderEnc ||
    !Number.isSafeInteger(source.uid) ||
    source.uid < 1 ||
    !Number.isSafeInteger(source.uidValidity) ||
    source.uidValidity < 1 ||
    !/^\d+(?:\.\d+)*$/.test(part)
  )
    throw new Error("INVALID_SOURCE");
  return `/api/v1/mailbox/folders/${encodeURIComponent(source.folderEnc)}/messages/${source.uid}/attachments/${part}?uid_validity=${source.uidValidity}`;
}

/** Bound the stream itself; Content-Length is advisory and may be absent. */
export async function boundedAttachment(
  source: AttachmentSource,
  part: string,
  signal: AbortSignal,
): Promise<Uint8Array> {
  const token = getMailboxToken();
  if (!token) throw new Error("AUTH_REQUIRED");
  const response = await fetch(attachmentURL(source, part), {
    headers: { Authorization: `Bearer ${token}` },
    signal,
    credentials: "omit",
    redirect: "error",
  });
  if (!response.ok) throw new Error("ATTACHMENT_UNAVAILABLE");
  if (Number(response.headers.get("content-length")) > PREVIEW_LIMIT) {
    await response.body?.cancel();
    throw new Error("PREVIEW_TOO_LARGE");
  }
  const reader = response.body?.getReader();
  if (!reader) throw new Error("ATTACHMENT_UNAVAILABLE");
  const parts: Uint8Array[] = [];
  let length = 0;
  try {
    while (true) {
      signal.throwIfAborted();
      const next = await reader.read();
      if (next.done) break;
      length += next.value.byteLength;
      if (length > PREVIEW_LIMIT) throw new Error("PREVIEW_TOO_LARGE");
      parts.push(next.value);
    }
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
  signal.throwIfAborted();
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const part of parts) {
    bytes.set(part, offset);
    offset += part.length;
  }
  return bytes;
}

/** Check dimensions before allowing browser image decoding. No animated formats. */
export function rasterDimensions(
  bytes: Uint8Array,
  mime: string,
): { width: number; height: number } {
  const data = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let width = 0,
    height = 0;
  if (mime === "image/png") {
    const signature = [137, 80, 78, 71, 13, 10, 26, 10];
    if (
      bytes.length < 33 ||
      !signature.every((n, i) => bytes[i] === n) ||
      data.getUint32(8) !== 13 ||
      data.getUint32(12) !== 0x49484452
    )
      throw new Error("INVALID_IMAGE");
    width = data.getUint32(16);
    height = data.getUint32(20);
    let offset = 8,
      ended = false;
    while (offset + 12 <= bytes.length) {
      const size = data.getUint32(offset),
        type = data.getUint32(offset + 4);
      if (
        size > bytes.length - offset - 12 ||
        [0x6163544c, 0x6663544c, 0x66644154].includes(type) ||
        (type === 0x49484452 && offset !== 8) ||
        (type === 0x49454e44 && size !== 0)
      )
        throw new Error("INVALID_IMAGE");
      offset += size + 12;
      if (type === 0x49454e44) {
        ended = true;
        break;
      }
    }
    if (!ended || offset !== bytes.length) throw new Error("INVALID_IMAGE");
  } else if (mime === "image/jpeg") {
    if (
      bytes.length < 4 ||
      bytes[0] !== 255 ||
      bytes[1] !== 216 ||
      bytes[bytes.length - 2] !== 255 ||
      bytes[bytes.length - 1] !== 217
    )
      throw new Error("INVALID_IMAGE");
    let offset = 2;
    let entropy = false;
    let ended = false;
    while (offset < bytes.length) {
      if (entropy) {
        const next = bytes.indexOf(255, offset);
        if (next < 0) throw new Error("INVALID_IMAGE");
        offset = next;
      }
      if (bytes[offset++] !== 255) throw new Error("INVALID_IMAGE");
      while (bytes[offset] === 255) offset++;
      if (offset >= bytes.length) throw new Error("INVALID_IMAGE");
      const marker = bytes[offset++];
      if (entropy && (marker === 0 || (marker >= 0xd0 && marker <= 0xd7)))
        continue;
      entropy = false;
      if (marker === 0xd9) {
        ended = offset === bytes.length;
        break;
      }
      if (marker === 0xd8 || marker === 0 || offset + 2 > bytes.length)
        throw new Error("INVALID_IMAGE");
      const size = data.getUint16(offset);
      if (size < 2 || offset + size > bytes.length)
        throw new Error("INVALID_IMAGE");
      if (
        marker >= 0xc0 &&
        marker <= 0xcf &&
        ![0xc4, 0xc8, 0xcc].includes(marker)
      ) {
        if (width || ![0xc0, 0xc1, 0xc2].includes(marker) || size < 8)
          throw new Error("INVALID_IMAGE");
        height = data.getUint16(offset + 3);
        width = data.getUint16(offset + 5);
      }
      offset += size;
      if (marker === 0xda) entropy = true;
    }
    if (!ended) throw new Error("INVALID_IMAGE");
  } else throw new Error("UNSUPPORTED_PREVIEW");
  if (
    !width ||
    !height ||
    width > 8192 ||
    height > 8192 ||
    width * height > PREVIEW_PIXELS
  )
    throw new Error("IMAGE_TOO_LARGE");
  return { width, height };
}
export type SafePreview =
  | { kind: "image"; dataURL: string; pixels: number }
  | { kind: "text"; text: string };
export function decodePreview(
  bytes: Uint8Array,
  attachment: AttachmentMeta,
): SafePreview {
  if (bytes.length > PREVIEW_LIMIT) throw new Error("PREVIEW_TOO_LARGE");
  const mime = attachment.content_type.split(";", 1)[0].trim().toLowerCase();
  if (previewType(mime) === "image") {
    const { width, height } = rasterDimensions(bytes, mime);
    let binary = "";
    for (let i = 0; i < bytes.length; i += 8192)
      binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
    return {
      kind: "image",
      dataURL: `data:${mime};base64,${btoa(binary)}`,
      pixels: width * height,
    };
  }
  if (TEXT_TYPES.has(mime))
    return {
      kind: "text",
      text: new TextDecoder("utf-8", { fatal: true }).decode(bytes),
    };
  throw new Error("UNSUPPORTED_PREVIEW");
}
