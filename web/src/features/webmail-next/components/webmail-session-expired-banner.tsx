import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { Clock } from "lucide-react";
import {
  getLastSessionReturnTo,
  isMailboxSessionExpired,
  MAILBOX_SESSION_EXPIRED_EVENT,
  type MailboxSessionExpiryInfo,
} from "@/features/webmail/mailbox-api";

/**
 * Global session-expiry recovery surface (REVIEW-UX-001 / REVIEW-FE-008).
 * The 401 funnel no longer hard-navigates: this banner mounts inside the
 * already-loaded webmail tree, announces the dead session and offers
 * re-login, carrying the user back to the route they were on via
 * `/webmail/login?next=…`. Renders nothing while the session is alive.
 */
export function WebmailSessionExpiredBanner() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [expired, setExpired] = useState(isMailboxSessionExpired);

  useEffect(() => {
    const onExpired = (): void => setExpired(isMailboxSessionExpired());
    window.addEventListener(MAILBOX_SESSION_EXPIRED_EVENT, onExpired);
    return () =>
      window.removeEventListener(MAILBOX_SESSION_EXPIRED_EVENT, onExpired);
  }, []);

  if (!expired) return null;

  const returnTo =
    getLastSessionReturnTo() ??
    `${window.location.pathname}${window.location.search}`;
  const goRelogin = () =>
    navigate(`/webmail/login?next=${encodeURIComponent(returnTo)}`, {
      replace: true,
    });

  return (
    <div
      role="alert"
      data-testid="session-expired-banner"
      className="fixed inset-x-0 top-0 z-[70] flex items-center justify-center gap-3 px-4 py-2 bg-[var(--vellum-danger-fill)] border-b border-[var(--vellum-danger-border)] text-xs text-[var(--vellum-danger-strong)]"
    >
      <Clock className="w-3.5 h-3.5 shrink-0" aria-hidden="true" />
      <span>{t("webmail_next.session_expired_banner")}</span>
      <button
        type="button"
        data-testid="session-expired-relogin"
        onClick={goRelogin}
        className="underline underline-offset-2 font-medium hover:opacity-80"
      >
        {t("webmail_next.session_expired_relogin")}
      </button>
    </div>
  );
}

/** Type helper re-export keeps the CustomEvent detail typed for listeners. */
export type { MailboxSessionExpiryInfo };
