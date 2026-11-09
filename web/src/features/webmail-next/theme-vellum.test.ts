import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The brand token gates the unread dot, the reading-row indicator bar and the
// list focus ring; when it is missing every consumer computes to transparent.
const css = readFileSync(
  join(process.cwd(), "src/features/webmail-next/theme-vellum.css"),
  "utf8",
);

describe("theme-vellum tokens", () => {
  it("defines --brand-primary in the root scope with the spec default", () => {
    const rootBlock = css.slice(
      css.indexOf(":root"),
      css.indexOf("}", css.indexOf(":root")),
    );
    expect(rootBlock).toMatch(/--brand-primary:\s*#2563eb/i);
  });

  // 批次 2 语义令牌（WEBMAIL-BATCH2-SPEC.md 2.1）：缺一个消费处就会解析为
  // transparent/inherit，且 dark v2 以这套令牌为唯一前置（决策①b）。
  it("defines all batch-2 semantic tokens with the spec values", () => {
    const rootBlock = css.slice(
      css.indexOf(":root"),
      css.indexOf("}", css.indexOf(":root")),
    );
    const spec = {
      "--vellum-brand-strong": "#1d4ed8",
      "--vellum-brand-soft": "#eff6ff",
      "--vellum-ink-disabled": "#d6d3d1",
      "--vellum-surface-muted": "#f6f4ee",
      "--vellum-warning-fill": "#fef3c7",
      "--vellum-warning-border": "#fde68a",
      "--vellum-warning-text": "#92400e",
      "--vellum-danger-fill": "#fee2e2",
      "--vellum-danger-fill-soft": "#fef2f2",
      "--vellum-danger-border": "#fecaca",
      "--vellum-danger-text": "#991b1b",
      "--vellum-danger-strong": "#dc2626",
      "--vellum-success-fill": "#f0fdf4",
      "--vellum-success-border": "#bbf7d0",
      "--vellum-success-text": "#166534",
    };
    for (const [token, value] of Object.entries(spec)) {
      expect(rootBlock, `missing ${token} (expected ${value})`).toMatch(
        new RegExp(`${token}:\\s*${value.replace("#", "#")}\\s*;`, "i"),
      );
    }
  });

  it("retires the batch-2 merge targets (info token removed, values confirmed)", () => {
    const rootBlock = css.slice(
      css.indexOf(":root"),
      css.indexOf("}", css.indexOf(":root")),
    );
    // --vellum-color-info 已删除（无消费方，归并 warning/danger）。
    expect(rootBlock).not.toMatch(/--vellum-color-info/);
    // 行悬停值按规格 2.1 注确认为 #f3f1ec（原 #f8f7f2）。
    expect(rootBlock).toMatch(/--vellum-row-hover:\s*#f3f1ec\s*;/i);
    // #3F3F46 归并 ink-secondary（ink-read 不再独立取色）。
    expect(rootBlock).toMatch(
      /--vellum-ink-read:\s*var\(--vellum-ink-secondary\)\s*;/i,
    );
  });
});
