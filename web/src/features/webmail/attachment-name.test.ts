import { describe, expect, it } from "vitest";
import { attachmentDisplayName, decodeRfc2047 } from "./attachment-name";

function b64utf8(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

describe("decodeRfc2047", () => {
  it("decodes UTF-8 base64 encoded words", () => {
    // =?utf-8?B?= for "résumé.pdf"
    const encoded = "=?utf-8?B?" + b64utf8("résumé.pdf") + "?=";
    expect(decodeRfc2047(encoded)).toBe("résumé.pdf");
  });

  it("decodes quoted-printable encoded words", () => {
    expect(decodeRfc2047("=?utf-8?q?caf=C3=A9.txt?=")).toBe("café.txt");
  });

  it("leaves plain names alone", () => {
    expect(decodeRfc2047("plain.pdf")).toBe("plain.pdf");
  });
});

describe("attachmentDisplayName", () => {
  it("strips unix and windows path components", () => {
    expect(attachmentDisplayName("../../etc/passwd", "3")).toBe("passwd");
    expect(attachmentDisplayName("..\\..\\win\\evil.exe", "3")).toBe(
      "evil.exe",
    );
  });

  it("drops control characters and trims", () => {
    expect(attachmentDisplayName("bad\u0000\u0007name.txt", "3")).toBe(
      "badname.txt",
    );
  });

  it("decodes leftover RFC 2047 encoded words", () => {
    const encoded = "=?utf-8?B?" + b64utf8("nøme.pdf") + "?=";
    expect(attachmentDisplayName(encoded, "3")).toBe("nøme.pdf");
  });

  it("falls back to the part id when nothing remains", () => {
    expect(attachmentDisplayName("", "3.1")).toBe("attachment-3.1");
    expect(attachmentDisplayName("..", "3.1")).toBe("attachment-3.1");
    expect(attachmentDisplayName("../../../", "3.1")).toBe("attachment-3.1");
  });

  it("caps very long names", () => {
    const long = "x".repeat(500) + ".pdf";
    expect(attachmentDisplayName(long, "3").length).toBeLessThanOrEqual(200);
  });
});
