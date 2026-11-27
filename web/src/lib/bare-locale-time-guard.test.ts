import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

/**
 * QA P3-3 regression guard: wall-clock timestamps must render through the
 * shared h23 formatters (`src/lib/format-h23.ts`), never through locale
 * calls on Date receivers — bare OR parameterized (QA pr165 P3-4 gap).
 * Number `.toLocaleString()` grouping is allowed.
 */
export function scanSource(src: string): string[] {
  const offenders: string[] = [];
  // 1. Time-typed calls are banned outright, bare or parameterized:
  //    toLocaleTimeString([...], opts) / toLocaleDateString(...)
  let m: RegExpExecArray | null;
  const timeRe = /\.toLocaleTimeString\(|\.toLocaleDateString\(/g;
  while ((m = timeRe.exec(src))) {
    offenders.push(`time-typed: ${src.slice(m.index, m.index + 70)}`);
  }
  // 2. toLocaleString is banned when the receiver is a Date — bare or
  //    parameterized. Non-Date receivers (numbers) are exempt.
  const strRe = /new Date\([^)]*\)\.toLocaleString\(/g;
  while ((m = strRe.exec(src))) {
    offenders.push(`date-receiver: ${src.slice(m.index, m.index + 70)}`);
  }
  return offenders;
}

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

  it("has no locale calls on Date receivers anywhere in src (incl. parameterized)", () => {
    const offenders: string[] = [];
    for (const file of walk(srcDir)) {
      if (file.endsWith("format-h23.ts")) continue; // documents the ban
      const rel = path.relative(srcDir, file);
      const src = fs.readFileSync(file, "utf8");
      for (const offender of scanSource(src)) {
        offenders.push(`${rel}: ${offender}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("keeps every remaining toLocaleString hit on a non-Date receiver", () => {
    const exempted: string[] = [];
    for (const file of walk(srcDir)) {
      if (file.endsWith("format-h23.ts")) continue; // documents the ban
      const rel = path.relative(srcDir, file);
      const src = fs.readFileSync(file, "utf8");
      let m: RegExpExecArray | null;
      const anyRe = /([A-Za-z0-9_.$[\]()?]+)\.toLocaleString\(/g;
      while ((m = anyRe.exec(src))) {
        if (m[1].includes("new Date")) continue;
        exempted.push(`${rel}: ${m[1]}`);
      }
    }
    // QA pr165 inventory: archive x2, dashboard x3, delivery x3 — number
    // grouping only. A count drift means a new call site needs review.
    expect(exempted.length).toBe(8);
  });

  it("is armed: parameterized Date-receiver calls are flagged (negative case)", () => {
    // Negative injection proving the pr165 gap is closed: the pre-P34
    // guard only matched no-arg forms; these parameterized shapes must be
    // flagged now.
    const fixture = [
      `const a = new Date(x).toLocaleTimeString([], { hour: "2-digit" });`,
      `const b = new Date(y).toLocaleString(i18n.language);`,
      `const c = new Date(z).toLocaleDateString("en-US");`,
      `const d = created.toLocaleTimeString([], opts);`,
    ].join("\n");
    const flagged = scanSource(fixture);
    // scanSource groups by class (time-typed first, then Date-receiver
    // toLocaleString) — assert by content, not index.
    expect(flagged.length).toBe(4);
    const dateReceiver = flagged.filter((f) => f.includes("date-receiver"));
    const timeTyped = flagged.filter((f) => f.includes("time-typed"));
    expect(dateReceiver.length).toBe(1);
    expect(dateReceiver[0]).toContain(".toLocaleString(i18n.language)");
    expect(timeTyped.length).toBe(3);
    // And a number receiver stays exempt.
    expect(scanSource(`const e = count.toLocaleString();`)).toEqual([]);
  });
});
