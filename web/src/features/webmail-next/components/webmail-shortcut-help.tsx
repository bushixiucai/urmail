import { useTranslation } from "react-i18next";

const GROUPS: Array<{ title: string; rows: Array<[string, string]> }> = [
  {
    title: "webmail_next.shortcut_group_move",
    rows: [
      ["j / k", "webmail_next.shortcut_move"],
      ["o / Enter", "webmail_next.shortcut_open"],
      ["g i", "webmail_next.shortcut_inbox"],
    ],
  },
  {
    title: "webmail_next.shortcut_group_act",
    rows: [
      ["x", "webmail_next.shortcut_check"],
      ["e", "webmail_next.shortcut_archive"],
      ["#", "webmail_next.shortcut_trash"],
      ["s", "webmail_next.shortcut_star"],
      ["Shift+I", "webmail_next.shortcut_read"],
      ["Shift+U", "webmail_next.shortcut_unread"],
    ],
  },
  {
    title: "webmail_next.shortcut_group_compose",
    rows: [
      ["c", "webmail_next.shortcut_compose"],
      ["r / a / f", "webmail_next.shortcut_reply"],
      ["⌘/Ctrl+Enter", "webmail_next.shortcut_send"],
      ["/", "webmail_next.shortcut_search"],
      ["?", "webmail_next.shortcut_help"],
    ],
  },
];

export function WebmailShortcutHelp({
  isOpen,
  onClose,
}: {
  isOpen: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  if (!isOpen) return null;
  return (
    <div
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/30"
      onMouseDown={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("webmail_next.shortcut_help_title")}
        data-testid="webmail-shortcut-help"
        className="w-[520px] max-w-[92vw] bg-white border border-[#EBE8E0] rounded-lg shadow-xl p-5"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-sm font-semibold text-[#18181B]">
            {t("webmail_next.shortcut_help_title")}
          </h2>
          <button
            type="button"
            data-testid="shortcut-help-close"
            onClick={onClose}
            className="text-xs text-[var(--vellum-ink-secondary)]"
          >
            Esc
          </button>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          {GROUPS.map((group) => (
            <section key={group.title}>
              <h3 className="text-xs font-semibold text-[#18181B] mb-2">
                {t(group.title)}
              </h3>
              <ul className="space-y-1.5">
                {group.rows.map(([keys, label]) => (
                  <li
                    key={keys}
                    className="flex items-center justify-between gap-2 text-xs"
                  >
                    <span className="font-mono text-[#18181B]">{keys}</span>
                    <span className="text-[var(--vellum-ink-secondary)] text-right">
                      {t(label)}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
