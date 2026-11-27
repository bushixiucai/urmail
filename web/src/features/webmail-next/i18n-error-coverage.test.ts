import { describe, it, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";

/**
 * UIUX-012 regression guard: every error code the API layer can emit must
 * have an `error.<CODE>` entry in BOTH locales, so the error surface never
 * renders a raw code (getApiErrorKey resolves `error.<CODE>` first).
 */
describe("backend error-code i18n coverage", () => {
  const repoRoot = path.resolve(__dirname, "../../../..");
  const apiDir = path.resolve(repoRoot, "internal/api");

  function collectBackendCodes(dir: string): Set<string> {
    const codes = new Set<string>();
    const walk = (d: string) => {
      for (const item of fs.readdirSync(d)) {
        const full = path.join(d, item);
        const stat = fs.statSync(full);
        if (stat.isDirectory()) {
          walk(full);
        } else if (item.endsWith(".go") && !item.endsWith("_test.go")) {
          const src = fs.readFileSync(full, "utf8");
          const re =
            /authError\([^)]*?,\s*http\.Status[A-Za-z]+,\s*"([A-Z][A-Z0-9_]+)"/g;
          let m: RegExpExecArray | null;
          while ((m = re.exec(src))) codes.add(m[1]);
        }
      }
    };
    walk(dir);
    return codes;
  }

  it("has error.<CODE> entries for every backend authError code in en and zh", () => {
    const codes = collectBackendCodes(apiDir);
    expect(codes.size).toBeGreaterThan(50);

    const en = JSON.parse(
      fs.readFileSync(
        path.resolve(repoRoot, "internal/i18n/locales/en.json"),
        "utf8",
      ),
    ) as Record<string, string>;
    const zh = JSON.parse(
      fs.readFileSync(
        path.resolve(repoRoot, "internal/i18n/locales/zh-CN.json"),
        "utf8",
      ),
    ) as Record<string, string>;

    const missingEn: string[] = [];
    const missingZh: string[] = [];
    for (const code of codes) {
      const key = `error.${code}`;
      if (!(key in en)) missingEn.push(key);
      if (!(key in zh)) missingZh.push(key);
    }
    expect(missingEn).toEqual([]);
    expect(missingZh).toEqual([]);
  });

  it("has no duplicate keys in the locale files", () => {
    for (const locale of ["en.json", "zh-CN.json"]) {
      const raw = fs.readFileSync(
        path.resolve(repoRoot, "internal/i18n/locales", locale),
        "utf8",
      );
      const seen = new Set<string>();
      const dupes: string[] = [];
      for (const line of raw.split("\n")) {
        const m = line.match(/^\s*"([^"]+)":/);
        if (!m) continue;
        if (seen.has(m[1])) dupes.push(m[1]);
        seen.add(m[1]);
      }
      expect(dupes).toEqual([]);
    }
  });
});
