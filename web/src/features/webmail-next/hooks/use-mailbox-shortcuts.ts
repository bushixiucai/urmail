import { useEffect, useRef } from "react";
import {
  isTextEntryTarget,
  resolveMailboxShortcut,
  type ShortcutAction,
} from "../lib/mailbox-shortcuts";

interface UseMailboxShortcutsOptions {
  composeOpen: boolean;
  overlayOpen: boolean;
  onAction: (action: ShortcutAction) => void;
}

/** One window listener for the workbench. Components must not add their own. */
export function useMailboxShortcuts({
  composeOpen,
  overlayOpen,
  onAction,
}: UseMailboxShortcutsOptions) {
  const goArmed = useRef(false);
  const state = useRef({ composeOpen, overlayOpen, onAction });
  state.current = { composeOpen, overlayOpen, onAction };

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const current = state.current;
      const decision = resolveMailboxShortcut({
        key: event.key,
        shift: event.shiftKey,
        meta: event.metaKey,
        ctrl: event.ctrlKey,
        alt: event.altKey,
        textEntry: isTextEntryTarget(event.target),
        composeOpen: current.composeOpen,
        overlayOpen: current.overlayOpen,
        goArmed: goArmed.current,
      });
      goArmed.current = decision.nextGoArmed;
      if (decision.action === "none") return;
      event.preventDefault();
      current.onAction(decision.action);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
}
