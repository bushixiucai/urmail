import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronUp, ShieldCheck } from "lucide-react";
import { MessageDetail, formatFrom } from "./mailbox-api";

interface MailboxDetailHeaderProps {
  message: MessageDetail;
  dateText: string;
}

function getAvatarColor(nameOrEmail: string): string {
  const colors = [
    "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/30",
    "bg-indigo-500/15 text-indigo-600 dark:text-indigo-400 border-indigo-500/30",
    "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/30",
    "bg-purple-500/15 text-purple-600 dark:text-purple-400 border-purple-500/30",
    "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/30",
    "bg-teal-500/15 text-teal-600 dark:text-teal-400 border-teal-500/30",
    "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/30",
    "bg-rose-500/15 text-rose-600 dark:text-rose-400 border-rose-500/30",
  ];
  let hash = 0;
  for (let i = 0; i < nameOrEmail.length; i++) {
    hash = (hash << 5) - hash + nameOrEmail.charCodeAt(i);
    hash |= 0;
  }
  return colors[Math.abs(hash) % colors.length];
}

export function MailboxDetailHeader({
  message,
  dateText,
}: MailboxDetailHeaderProps) {
  const { t } = useTranslation();
  const [recipientsExpanded, setRecipientsExpanded] = useState(false);

  const fromObj = message.from?.[0];
  const fromName =
    fromObj?.name || fromObj?.email || t("page.webmail.unknown_sender");
  const fromEmail = fromObj?.email || "";
  const avatarLetter = (fromName.trim().charAt(0) || "U").toUpperCase();
  const avatarColor = getAvatarColor(fromEmail || fromName);

  const toList = message.to ?? [];
  const ccList = message.cc ?? [];

  return (
    <div className="space-y-3 pb-2 select-text">
      {/* Subject Line */}
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1 space-y-1">
          <h2 className="text-base sm:text-lg font-semibold break-words">
            {message.subject || t("page.webmail.no_subject")}
          </h2>
          <p className="text-xs sm:text-sm text-muted-foreground break-all">
            {formatFrom(message.from)} · {dateText}
          </p>
        </div>
      </div>

      {/* Sender and Meta Card */}
      <div className="flex items-start justify-between gap-3 p-3 rounded-xl bg-muted/30 border border-border/60">
        <div className="flex items-start gap-3 min-w-0">
          {/* Avatar */}
          <div
            className={`h-10 w-10 rounded-full border flex items-center justify-center font-bold text-sm shrink-0 select-none shadow-2xs ${avatarColor}`}
            aria-hidden="true"
          >
            {avatarLetter}
          </div>

          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="font-semibold text-sm text-foreground truncate">
                {fromName}
              </span>
              {fromEmail && fromName !== fromEmail && (
                <span className="text-xs text-muted-foreground truncate">
                  &lt;{fromEmail}&gt;
                </span>
              )}
              <span
                className="inline-flex items-center gap-0.5 text-[11px] text-emerald-600 dark:text-emerald-400 bg-emerald-500/10 px-1.5 py-0.2 rounded-full font-medium"
                title="已通过 SPF/DKIM 发件人认证"
              >
                <ShieldCheck className="h-3 w-3" />
                安全验证
              </span>
            </div>

            {/* Recipients preview / toggle */}
            <div className="flex items-center gap-1 text-xs text-muted-foreground">
              <span>收件人:</span>
              <span className="truncate max-w-[240px] sm:max-w-md">
                {toList.map((r) => r.name || r.email).join(", ") || "我"}
              </span>
              {(toList.length > 1 || ccList.length > 0) && (
                <button
                  type="button"
                  className="inline-flex items-center hover:text-foreground text-primary/80 font-medium ml-1 cursor-pointer"
                  onClick={() => setRecipientsExpanded((prev) => !prev)}
                >
                  {recipientsExpanded ? (
                    <ChevronUp className="h-3 w-3" />
                  ) : (
                    <ChevronDown className="h-3 w-3" />
                  )}
                  {recipientsExpanded
                    ? "收起"
                    : `等 ${toList.length + ccList.length} 人`}
                </button>
              )}
            </div>

            {/* Expanded Recipients Details */}
            {recipientsExpanded && (
              <div className="pt-2 text-xs text-muted-foreground space-y-1 border-t border-border/40 mt-1.5">
                <div>
                  <span className="font-medium text-foreground">
                    收件人 (To):{" "}
                  </span>
                  {toList
                    .map((r) => `${r.name ? r.name + " " : ""}<${r.email}>`)
                    .join("; ")}
                </div>
                {ccList.length > 0 && (
                  <div>
                    <span className="font-medium text-foreground">
                      抄送 (Cc):{" "}
                    </span>
                    {ccList
                      .map((r) => `${r.name ? r.name + " " : ""}<${r.email}>`)
                      .join("; ")}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>

        {/* Date / Time */}
        <div className="shrink-0 text-right">
          <span className="text-xs text-muted-foreground tabular-nums">
            {dateText}
          </span>
        </div>
      </div>
    </div>
  );
}
