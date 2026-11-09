// web/src/features/webmail-next/lib/avatar-initial.ts

// F4: sender display names occasionally arrive as mangled RFC 2047
// encoded-words (e.g. "=?utf-8?B?...?=") whose raw bytes render as tofu
// blocks in the avatar circle. Pick the first renderable letter/number
// instead; fall back to the email local part, then a neutral glyph.
const ENCODED_WORD = /=\?[^?\s]+\?[BbQq]\?[^?]*\?=/g;
const SAFE_INITIAL = /^[\p{L}\p{N}]$/u;

function firstSafeChar(value: string | null | undefined): string | null {
  const name = (value ?? "").trim();
  for (const ch of Array.from(name)) {
    if (SAFE_INITIAL.test(ch)) return ch.toUpperCase();
  }
  return null;
}

export function avatarInitial(
  raw: string | null | undefined,
  fallback?: string | null,
): string {
  let name = (raw ?? "").trim();
  if (name.includes("=?")) {
    name = name.replace(ENCODED_WORD, "").trim();
  }
  return firstSafeChar(name) ?? firstSafeChar(fallback) ?? "@";
}
