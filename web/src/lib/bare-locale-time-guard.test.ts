import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

/**
 * QA P3-3 regression guard: wall-clock timestamps must render through the
 * shared h23 formatters (`src/lib/format-h23.ts`), never through bare
 * locale-default calls. Number `.toLocaleString()` grouping is allowed.
 */
describe("bare locale time-call guard", () => {
  const srcDir = path.resolve(__dirname, "..");

  function walk(dir: string): string[] {
    const out: string[] = [];
    for (const item of fs.readdirSync(dir)) {
      const full = path.join(dir, item);
      const stat = fs.statSync(full);
      if (stat.isDirectory()) {
        out.push(...walk(full));
      } else if (
        (item.endsWith(".ts") || item.endsWith(".tsx")) &&
        !item.includes(".test.")
      ) {
        out.push(full);
      }
    }
    return out;
  }

  it("has no bare time-typed toLocale* calls in src", () => {
    const offenders: string[] = [];
    for (const file of walk(srcDir)) {
      const rel = path.relative(srcDir, file);
      const src = fs.readFileSync(file, "utf8");
      // 1. Any toLocaleTimeString/toLocaleDateString without an explicit
      //    locale argument is a wall-clock call — banned outright.
      if (/\.toLocaleTimeString\(\)|\.toLocaleDateString\(\)/.test(src)) {
        offenders.push(rel);
        continue;
      }
      // 2. toLocaleString() is only allowed on numbers — a `new Date(...)`
      //    receiver is a wall-clock call and banned.
      if (/new Date\([^)]*\)\.toLocaleString\(\)/.test(src)) {
        offenders.push(rel);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("formats every remaining toLocaleString hit through a number, not a date", () => {
    for (const file of walk(srcDir)) {
      const src = fs.readFileSync(file, "utf8");
      const bare = src.match(/([A-Za-z0-9_.[]+)\.toLocaleString\(\)/g) ?? [];
      for (const hit of bare) {
        expect(hit.startsWith("new Date("), `${file}: ${hit}`).toBe(false);
      }
    }
  });
});
