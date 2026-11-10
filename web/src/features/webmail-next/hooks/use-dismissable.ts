import { useEffect } from "react";
import type { RefObject } from "react";

/**
 * Shared dismissal for lightweight menus/popovers (B3-1): close on
 * outside mousedown and on Escape, returning focus to the trigger button
 * so keyboard users are not dropped at document.body. Listens with
 * capture on window, matching the row/folder context-menu convention.
 */
export function useDismissable(
  open: boolean,
  containerRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  triggerRef?: RefObject<HTMLElement | null>,
): void {
  useEffect(() => {
    if (!open) return;
    const handleMouseDown = (event: MouseEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) {
        onClose();
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
        // Return focus to the trigger. If the trigger is display:none
        // (e.g. the md:hidden ⋯ menu on desktop) focus() is a silent
        // no-op and the browser drops focus on <body> — after the close
        // re-render, re-focus only when the trigger is actually visible
        // (QA Frontend-N1).
        const trigger = triggerRef?.current;
        trigger?.focus();
        requestAnimationFrame(() => {
          if (
            (document.activeElement === null ||
              document.activeElement === document.body) &&
            triggerRef?.current &&
            triggerRef.current.offsetParent !== null
          ) {
            triggerRef.current.focus();
          }
        });
      }
    };
    window.addEventListener("mousedown", handleMouseDown, true);
    window.addEventListener("keydown", handleKeyDown, true);
    return () => {
      window.removeEventListener("mousedown", handleMouseDown, true);
      window.removeEventListener("keydown", handleKeyDown, true);
    };
  }, [open, containerRef, onClose, triggerRef]);
}
