import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

export type MailboxShortcut =
  | "compose"
  | "reply"
  | "replyAll"
  | "forward"
  | "trash"
  | "next"
  | "previous"
  | "search"
  | "close";
export function shortcutFor(
  event: KeyboardEvent,
): MailboxShortcut | "help" | null {
  if (
    event.defaultPrevented ||
    event.isComposing ||
    event.repeat ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey
  )
    return null;
  const target = event.target;
  if (
    target instanceof Element &&
    target.closest(
      'input,textarea,select,[contenteditable]:not([contenteditable="false"]),[role="textbox"]',
    )
  )
    return null;
  const keys: Record<string, MailboxShortcut | "help"> = {
    c: "compose",
    r: "reply",
    a: "replyAll",
    f: "forward",
    Delete: "trash",
    j: "next",
    k: "previous",
    "/": "search",
    Escape: "close",
    "?": "help",
  };
  return keys[event.key] ?? null;
}
export function MailboxKeyboardShortcuts({
  disabled,
  onAction,
}: {
  disabled: boolean;
  onAction: (action: MailboxShortcut) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [enabled, setEnabled] = useState(() => {
    try {
      return localStorage.getItem("urmail.mailbox-shortcuts") !== "off";
    } catch {
      return true;
    }
  });
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (
        disabled ||
        open ||
        document.querySelector('[role="dialog"], [role="alertdialog"]')
      )
        return;
      const action = shortcutFor(event);
      if (!action || (!enabled && action !== "help")) return;
      event.preventDefault();
      if (action === "help") setOpen(true);
      else onAction(action);
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [disabled, open, enabled, onAction]);
  const rows: [string, string][] = [
    ["C", "compose"],
    ["R", "reply"],
    ["A", "replyAll"],
    ["F", "forward"],
    ["Delete", "trash"],
    ["J / K", "navigate"],
    ["/", "search"],
    ["Esc", "close"],
    ["?", "help"],
  ];
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" className="min-h-11 md:min-h-8">
          {t("page.webmail.shortcuts_title")}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("page.webmail.shortcuts_title")}</DialogTitle>
          <DialogDescription>
            {t("page.webmail.shortcuts_scope")}
          </DialogDescription>
        </DialogHeader>
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => {
              setEnabled(event.target.checked);
              try {
                localStorage.setItem(
                  "urmail.mailbox-shortcuts",
                  event.target.checked ? "on" : "off",
                );
              } catch {
                /* Storage may be unavailable. */
              }
            }}
          />
          {t("page.webmail.shortcuts_enabled")}
        </label>
        <dl className="grid grid-cols-[auto_1fr] gap-x-5 gap-y-2 text-sm">
          {rows.map(([key, label]) => (
            <div key={key} className="contents">
              <dt>
                <kbd className="rounded border px-1.5 py-0.5 font-mono">
                  {key}
                </kbd>
              </dt>
              <dd>{t(`page.webmail.shortcut_${label}`)}</dd>
            </div>
          ))}
        </dl>
      </DialogContent>
    </Dialog>
  );
}
