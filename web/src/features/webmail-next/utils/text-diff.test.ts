import { describe, it, expect } from "vitest";
import { computeTextDiff } from "./text-diff";

describe("computeTextDiff", () => {
  it("returns empty array for two empty strings", () => {
    expect(computeTextDiff("", "")).toEqual([]);
  });

  it("returns single eq chunk for identical strings", () => {
    expect(computeTextDiff("Hello world", "Hello world")).toEqual([
      { type: "eq", value: "Hello world" },
    ]);
  });

  it("handles complete replacement", () => {
    expect(computeTextDiff("old text", "new text")).toEqual([
      { type: "del", value: "old" },
      { type: "add", value: "new" },
      { type: "eq", value: " text" },
    ]);
  });

  it("handles insertions and deletions within sentences", () => {
    const diff = computeTextDiff(
      "已经找对方退回了。",
      "经技术对账已获全额冲抵，新签协议可锁38%折扣。",
    );
    expect(diff.some((d) => d.type === "del")).toBe(true);
    expect(diff.some((d) => d.type === "add")).toBe(true);
  });
});
