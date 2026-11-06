import { describe, it, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";

describe("i18n Code Key Coverage (D2 Regression Guard)", () => {
  const rootDir = path.resolve(__dirname, "..");
  const repoRoot = path.resolve(rootDir, "../../../");
  const zhPath = path.resolve(repoRoot, "internal/i18n/locales/zh-CN.json");
  const enPath = path.resolve(repoRoot, "internal/i18n/locales/en.json");

  it("verifies that all t() keys referenced in webmail-next code exist in both zh-CN and en locales", () => {
    expect(fs.existsSync(zhPath)).toBe(true);
    expect(fs.existsSync(enPath)).toBe(true);

    const zhCatalog = JSON.parse(fs.readFileSync(zhPath, "utf8"));
    const enCatalog = JSON.parse(fs.readFileSync(enPath, "utf8"));

    const webmailNextDir = path.resolve(__dirname);
    const keyPattern =
      /\bt\(\s*["']([a-zA-Z0-9_.-]+)["'](?:\s*,\s*(?:["'][\s\S]*?["']|\{[\s\S]*?\}))?/g;

    function getCodeFiles(dir: string): string[] {
      let results: string[] = [];
      const list = fs.readdirSync(dir);
      for (const item of list) {
        const full = path.join(dir, item);
        const stat = fs.statSync(full);
        if (stat.isDirectory()) {
          results = results.concat(getCodeFiles(full));
        } else if (
          (item.endsWith(".tsx") || item.endsWith(".ts")) &&
          !item.endsWith(".test.tsx") &&
          !item.endsWith(".test.ts")
        ) {
          results.push(full);
        }
      }
      return results;
    }

    const files = getCodeFiles(webmailNextDir);
    expect(files.length).toBeGreaterThan(10);

    const missingInZh: { file: string; key: string }[] = [];
    const missingInEn: { file: string; key: string }[] = [];
    const scannedKeys = new Set<string>();

    for (const filePath of files) {
      const content = fs.readFileSync(filePath, "utf8");
      let match: RegExpExecArray | null;
      while ((match = keyPattern.exec(content)) !== null) {
        const key = match[1];
        scannedKeys.add(key);
        if (!(key in zhCatalog)) {
          missingInZh.push({
            file: path.relative(webmailNextDir, filePath),
            key,
          });
        }
        if (!(key in enCatalog)) {
          missingInEn.push({
            file: path.relative(webmailNextDir, filePath),
            key,
          });
        }
      }
    }

    expect(scannedKeys.size).toBeGreaterThan(50);
    expect(missingInZh).toEqual([]);
    expect(missingInEn).toEqual([]);
  });

  it("verifies no hardcoded Chinese characters in fallback arguments within webmail-next components", () => {
    const webmailNextDir = path.resolve(__dirname);
    const chinesePattern = /[一-龥]/;

    function getCodeFiles(dir: string): string[] {
      let results: string[] = [];
      const list = fs.readdirSync(dir);
      for (const item of list) {
        const full = path.join(dir, item);
        const stat = fs.statSync(full);
        if (stat.isDirectory()) {
          results = results.concat(getCodeFiles(full));
        } else if (
          (item.endsWith(".tsx") || item.endsWith(".ts")) &&
          !item.endsWith(".test.tsx") &&
          !item.endsWith(".test.ts")
        ) {
          results.push(full);
        }
      }
      return results;
    }

    const files = getCodeFiles(webmailNextDir);
    const violations: { file: string; line: number; text: string }[] = [];

    for (const filePath of files) {
      const lines = fs.readFileSync(filePath, "utf8").split("\n");
      lines.forEach((line, index) => {
        const trimmed = line.trim();
        // Ignore comments
        if (
          trimmed.startsWith("//") ||
          trimmed.startsWith("/*") ||
          trimmed.startsWith("*")
        ) {
          return;
        }
        if (chinesePattern.test(line)) {
          violations.push({
            file: path.relative(webmailNextDir, filePath),
            line: index + 1,
            text: trimmed,
          });
        }
      });
    }

    // Zero hardcoded Chinese in code logic or fallback strings
    expect(violations).toEqual([]);
  });
});
