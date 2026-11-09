import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { useDismissable } from "./use-dismissable";

function Harness({ onClose }: { onClose: () => void }) {
  const [open, setOpen] = useState(true);
  const containerRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  useDismissable(
    open,
    containerRef,
    () => {
      setOpen(false);
      onClose();
    },
    triggerRef,
  );
  return (
    <div>
      <button ref={triggerRef} data-testid="trigger">
        trigger
      </button>
      <div ref={containerRef} data-testid="container">
        {open && <span data-testid="menu">menu</span>}
      </div>
      <span data-testid="outside">outside</span>
    </div>
  );
}

afterEach(() => {
  cleanup();
});

describe("useDismissable", () => {
  it("closes on Escape and returns focus to the trigger", () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    screen.getByTestId("trigger").focus();
    expect(screen.getByTestId("menu")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("menu")).toBeNull();
    expect(document.activeElement).toBe(screen.getByTestId("trigger"));
  });

  it("closes on outside mousedown but not on clicks inside the container", () => {
    const onClose = vi.fn();
    render(<Harness onClose={onClose} />);
    fireEvent.mouseDown(screen.getByTestId("container"), { bubbles: true });
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(screen.getByTestId("outside"), { bubbles: true });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps listeners off while closed", () => {
    const onClose = vi.fn();
    const { rerender } = render(<Harness onClose={onClose} />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    rerender(<Harness onClose={onClose} />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
