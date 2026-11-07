import { describe, expect, it } from "vitest";
import { resolveMailboxShortcut } from "./mailbox-shortcuts";

const idle = {
  shift: false,
  meta: false,
  ctrl: false,
  alt: false,
  textEntry: false,
  composeOpen: false,
  overlayOpen: false,
  goArmed: false,
};

describe("resolveMailboxShortcut", () => {
  it("moves with j/k and does not treat them as open", () => {
    expect(resolveMailboxShortcut({ ...idle, key: "j" }).action).toBe(
      "cursor-next",
    );
    expect(resolveMailboxShortcut({ ...idle, key: "k" }).action).toBe(
      "cursor-prev",
    );
    expect(resolveMailboxShortcut({ ...idle, key: "o" }).action).toBe("open");
    expect(resolveMailboxShortcut({ ...idle, key: "Enter" }).action).toBe(
      "open",
    );
  });

  it("maps archive, trash, star, and read state", () => {
    expect(resolveMailboxShortcut({ ...idle, key: "x" }).action).toBe(
      "toggle-check",
    );
    expect(resolveMailboxShortcut({ ...idle, key: "e" }).action).toBe(
      "archive",
    );
    expect(resolveMailboxShortcut({ ...idle, key: "#" }).action).toBe("trash");
    expect(resolveMailboxShortcut({ ...idle, key: "s" }).action).toBe(
      "toggle-star",
    );
    expect(
      resolveMailboxShortcut({ ...idle, key: "I", shift: true }).action,
    ).toBe("mark-read");
    expect(
      resolveMailboxShortcut({ ...idle, key: "U", shift: true }).action,
    ).toBe("mark-unread");
  });

  it("arms g then i for the inbox", () => {
    const armed = resolveMailboxShortcut({ ...idle, key: "g" });
    expect(armed.action).toBe("none");
    expect(armed.nextGoArmed).toBe(true);
    expect(
      resolveMailboxShortcut({ ...idle, key: "i", goArmed: true }).action,
    ).toBe("go-inbox");
  });

  it("does not hijack character keys in a field, compose, or an overlay", () => {
    expect(
      resolveMailboxShortcut({ ...idle, key: "j", textEntry: true }).action,
    ).toBe("none");
    expect(
      resolveMailboxShortcut({ ...idle, key: "e", composeOpen: true }).action,
    ).toBe("none");
    expect(
      resolveMailboxShortcut({ ...idle, key: "x", overlayOpen: true }).action,
    ).toBe("none");
  });

  it("sends with cmd/ctrl enter and closes compose on escape", () => {
    expect(
      resolveMailboxShortcut({
        ...idle,
        key: "Enter",
        meta: true,
        composeOpen: true,
        textEntry: true,
      }).action,
    ).toBe("send");
    expect(
      resolveMailboxShortcut({
        ...idle,
        key: "Escape",
        composeOpen: true,
        textEntry: true,
      }).action,
    ).toBe("close-compose");
    expect(
      resolveMailboxShortcut({
        ...idle,
        key: "Escape",
        composeOpen: true,
        overlayOpen: true,
      }).action,
    ).toBe("close-overlay");
  });

  it("opens help on question mark and focuses search on slash", () => {
    expect(resolveMailboxShortcut({ ...idle, key: "?" }).action).toBe("help");
    expect(resolveMailboxShortcut({ ...idle, key: "/" }).action).toBe(
      "focus-search",
    );
  });
});
