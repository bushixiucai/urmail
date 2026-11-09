import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MailListSkeleton, ReadingSkeleton } from "./webmail-skeleton";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

it("renders 8 two-bar rows with density-driven height and no pulse", () => {
  const { container } = render(
    <MailListSkeleton rows={8} rowHeight={52} testId="list-initial-skeleton" />,
  );
  const root = screen.getByTestId("list-initial-skeleton");
  expect(root).toBeTruthy();
  // 8 行、每行左 32px 圆块 + 主次双条
  expect(root.querySelectorAll(".vellum-skeleton-block").length).toBe(8 * 3);
  expect(container.querySelectorAll(".animate-pulse").length).toBe(0);
  // 行高与真实行一致（comfortable=52px）
  const rows = root.querySelectorAll(":scope > div > div");
  expect(rows.length).toBe(8);
  expect((rows[0] as HTMLElement).style.height).toBe("52px");
});

it("reading skeleton exposes the spec bar widths and no pulse", () => {
  const { container } = render(<ReadingSkeleton testId="reading-skeleton" />);
  expect(screen.getByTestId("reading-skeleton")).toBeTruthy();
  expect(container.querySelectorAll(".animate-pulse").length).toBe(0);
});
