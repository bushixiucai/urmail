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
});
