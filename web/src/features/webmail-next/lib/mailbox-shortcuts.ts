export type ShortcutAction =
  | "none"
  | "cursor-next"
  | "cursor-prev"
  | "open"
  | "toggle-check"
  | "archive"
  | "trash"
  | "reply"
  | "reply-all"
  | "forward"
  | "compose"
  | "toggle-star"
  | "mark-read"
  | "mark-unread"
  | "focus-search"
  | "help"
  | "go-inbox"
  | "send"
  | "close-compose"
  | "close-overlay";

export interface ShortcutInput {
  key: string;
  shift: boolean;
  meta: boolean;
  ctrl: boolean;
  alt: boolean;
  textEntry: boolean;
  composeOpen: boolean;
  overlayOpen: boolean;
  goArmed: boolean;
}

export interface ShortcutDecision {
  action: ShortcutAction;
  nextGoArmed: boolean;
}

export function isTextEntryTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  const tag = target.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (target instanceof HTMLElement && target.isContentEditable) return true;
  return Boolean(
    target.closest(
      "[data-compose-dock], [data-settings-drawer], [data-contacts-modal]",
    ),
  );
}

function none(goArmed = false): ShortcutDecision {
  return { action: "none", nextGoArmed: goArmed };
}

function decide(action: ShortcutAction, nextGoArmed = false): ShortcutDecision {
  return { action, nextGoArmed };
}

/**
 * Gmail-style single keys. Character keys are ignored in fields, compose,
 * and settings. j/k only move the list cursor.
 */
export function resolveMailboxShortcut(input: ShortcutInput): ShortcutDecision {
  const key = input.key;
  const letter = key.length === 1 ? key.toLowerCase() : key;

  if (input.overlayOpen) {
    if (key === "Escape") return decide("close-overlay");
    return none();
  }

  if (input.composeOpen) {
    if ((input.meta || input.ctrl) && key === "Enter" && !input.alt) {
      return decide("send");
    }
    if (key === "Escape") return decide("close-compose");
    return none();
  }

  if (input.textEntry || input.alt) return none();

  if (input.meta || input.ctrl) return none();

  if (input.goArmed && !input.shift && letter === "i") {
    return decide("go-inbox");
  }

  if (!input.shift && letter === "g") return none(true);

  const armed = false;
  if (input.shift && letter === "i") return decide("mark-read", armed);
  if (input.shift && letter === "u") return decide("mark-unread", armed);
  if (input.shift) return none();

  switch (letter) {
    case "j":
      return decide("cursor-next");
    case "k":
      return decide("cursor-prev");
    case "o":
    case "Enter":
      return decide("open");
    case "x":
      return decide("toggle-check");
    case "e":
      return decide("archive");
    case "#":
      return decide("trash");
    case "r":
      return decide("reply");
    case "a":
      return decide("reply-all");
    case "f":
      return decide("forward");
    case "c":
      return decide("compose");
    case "s":
      return decide("toggle-star");
    case "/":
      return decide("focus-search");
    case "?":
      return decide("help");
    case "Escape":
      return none();
    default:
      return none();
  }
}
