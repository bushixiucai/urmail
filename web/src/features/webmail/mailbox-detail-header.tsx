import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ChevronDown,
  ChevronUp,
  Shield,
  ShieldAlert,
  ShieldCheck,
} from "lucide-react";
import { MessageDetail } from "./mailbox-api";

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
  const { t, i18n } = useTranslation();
  const recipientsId = useId();
  const authentication = message.authentication;
  const authenticatedSource = authentication?.source === "urmail-receive-dkim";
  const status = authenticatedSource ? authentication.status : "unknown";
  const reason = authenticatedSource
    ? authentication.reason
    : authentication?.reason?.startsWith("receive_proof_")
      ? authentication.reason
      : "receive_proof_missing";
  const AuthIcon =
    status === "pass" ? ShieldCheck : status === "fail" ? ShieldAlert : Shield;
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
        </div>
      </div>

      {/* Sender and Meta Card */}
      <div className="flex flex-wrap items-start justify-between gap-3 p-3 rounded-xl bg-muted/30 border border-border/60">
        <div className="flex flex-1 items-start gap-3 min-w-0">
          {/* Avatar */}
          <div
            className={`h-10 w-10 rounded-full border flex items-center justify-center font-bold text-sm shrink-0 select-none shadow-2xs ${avatarColor}`}
            aria-hidden="true"
          >
            {avatarLetter}
          </div>

          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="flex items-center gap-1.5 flex-wrap">
              <span
                className={`font-semibold text-sm text-foreground ${fromName === fromEmail ? "break-all" : "truncate"}`}
              >
                {fromName}
              </span>
              {fromEmail && fromName !== fromEmail && (
                <span className="text-xs text-muted-foreground break-all">
                  &lt;{fromEmail}&gt;
                </span>
              )}
              <details className="w-full text-xs">
                <summary
                  className={`inline-flex cursor-pointer items-center gap-1 rounded px-1.5 py-1 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${status === "pass" ? "text-emerald-700 dark:text-emerald-400 bg-emerald-500/10" : status === "fail" ? "text-destructive bg-destructive/10" : "text-muted-foreground bg-muted"}`}
                >
                  <AuthIcon className="h-3.5 w-3.5" aria-hidden />
                  {t(`page.webmail.auth_dkim_${status}`)}
                  <span className="sr-only">
                    {" "}
                    · {t("page.webmail.auth_details")}
                  </span>
                </summary>
                <dl className="mt-2 space-y-1 break-words rounded border p-2 text-muted-foreground">
                  <div>
                    <dt className="inline font-medium">
                      {t("page.webmail.auth_source")}:{" "}
                    </dt>
                    <dd className="inline">
                      {authenticatedSource
                        ? t("page.webmail.auth_receiver_source")
                        : t("page.webmail.auth_dkim_unknown")}
                    </dd>
                  </div>
                  <div>
                    <dt className="sr-only">
                      {t("page.webmail.auth_details")}
                    </dt>
                    <dd>
                      {t(`page.webmail.auth_reason_${reason}`, {
                        defaultValue: t(
                          "page.webmail.auth_reason_receive_proof_invalid",
                        ),
                      })}
                    </dd>
                  </div>
                  {authenticatedSource && authentication.domain && (
                    <div>
                      <dt className="inline font-medium">
                        {t("page.webmail.auth_domain")}:{" "}
                      </dt>
                      <dd className="inline">{authentication.domain}</dd>
                    </div>
                  )}
                  {authenticatedSource && authentication.checked_at && (
                    <div>
                      <dt className="inline font-medium">
                        {t("page.webmail.auth_checked_at")}:{" "}
                      </dt>
                      <dd className="inline">
                        <time dateTime={authentication.checked_at}>
                          {new Date(authentication.checked_at).toLocaleString(
                            i18n.language,
                          )}
                        </time>
                      </dd>
                    </div>
                  )}
                  <div>
                    <dt className="sr-only">SPF</dt>
                    <dd>{t("page.webmail.auth_spf_unknown")}</dd>
                  </div>
                </dl>
              </details>
            </div>

            {/* Recipients preview / toggle */}
            <div className="flex items-center gap-1 text-xs text-muted-foreground">
              <span>{t("page.webmail.compose_to")}:</span>
              <span className="truncate min-w-0 max-w-[min(15rem,40vw)] sm:max-w-md">
                {toList.map((r) => r.name || r.email).join(", ") ||
                  t("page.webmail.recipients_empty")}
              </span>
              {(toList.length > 1 || ccList.length > 0) && (
                <button
                  type="button"
                  className="inline-flex items-center hover:text-foreground text-primary/80 font-medium ml-1 cursor-pointer"
                  aria-expanded={recipientsExpanded}
                  aria-controls={recipientsId}
                  onClick={() => setRecipientsExpanded((prev) => !prev)}
                >
                  {recipientsExpanded ? (
                    <ChevronUp className="h-3 w-3" />
                  ) : (
                    <ChevronDown className="h-3 w-3" />
                  )}
                  {recipientsExpanded
                    ? t("page.webmail.recipients_collapse")
                    : t("page.webmail.recipients_expand", {
                        count: toList.length + ccList.length,
                      })}
                </button>
              )}
            </div>

            {/* Expanded Recipients Details */}
            {recipientsExpanded && (
              <div
                id={recipientsId}
                className="pt-2 text-xs text-muted-foreground space-y-1 border-t border-border/40 mt-1.5 break-all"
              >
                <div>
                  <span className="font-medium text-foreground">
                    {t("page.webmail.compose_to")}:{" "}
                  </span>
                  {toList
                    .map((r) => `${r.name ? r.name + " " : ""}<${r.email}>`)
                    .join("; ")}
                </div>
                {ccList.length > 0 && (
                  <div>
                    <span className="font-medium text-foreground">
                      {t("page.webmail.compose_cc")}:{" "}
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
        <div className="w-full text-right">
          <span className="text-xs text-muted-foreground tabular-nums">
            {dateText}
          </span>
        </div>
      </div>
    </div>
  );
}
