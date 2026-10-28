import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { getMailboxToken, mailboxRequest } from "./mailbox-api";

export type CalendarInvitationAction =
  | "accept"
  | "decline"
  | "tentative"
  | "apply_update"
  | "apply_cancel"
  | "apply_reply"
  | "copy_personal";
export interface CalendarMailInvitation {
  source_digest: string;
  notification_status?: "none" | "sent" | "uncertain";
  verification: "verified" | "unverified";
  verification_reason: string;
  method: "REQUEST" | "REPLY" | "CANCEL";
  uid: string;
  sequence: number;
  organizer: string;
  from: string;
  recipient: string;
  recurrence_id: string;
  event: {
    summary: string;
    description: string;
    location: string;
    starts_at: string;
    ends_at: string;
    timezone: string;
    all_day: boolean;
    rrule: string;
  } | null;
  linked_event: { id: number; version: number; response: string } | null;
  actions: CalendarInvitationAction[];
}
interface Props {
  folderEnc: string;
  uid: number;
  uidValidity: number;
  onCalendarChanged?: () => void;
}
export function MailboxInvitationPanel(props: Props) {
  return (
    <InvitationPanelBody
      key={`${props.folderEnc}:${props.uidValidity}:${props.uid}`}
      {...props}
    />
  );
}
function InvitationPanelBody(props: Props) {
  const { t, i18n } = useTranslation();
  const cache = useQueryClient();
  const apiError = useApiErrorMessage();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [completed, setCompleted] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const source = {
    folder_enc: props.folderEnc,
    uid: props.uid,
    uid_validity: props.uidValidity,
  };
  const sourceKey = `${props.folderEnc}:${props.uidValidity}:${props.uid}`;
  const key = ["mailbox-invitation", getMailboxToken(), sourceKey] as const;
  const invitation = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      mailboxRequest<CalendarMailInvitation>(
        `/api/v1/mailbox/calendars/invitations?${new URLSearchParams({ folder_enc: props.folderEnc, uid: String(props.uid), uid_validity: String(props.uidValidity) })}`,
        { signal },
      ),
    enabled: props.uid > 0 && props.uidValidity > 0,
    retry: false,
  });
  const data = invitation.data;
  const apply = async (action: CalendarInvitationAction) => {
    if (!data || !confirmed || pending || completed === sourceKey) return;
    setPending(true);
    setError("");
    try {
      const result = await mailboxRequest<{
        applied: boolean;
        notification_status: string;
        sent_copy?: string;
      }>("/api/v1/mailbox/calendars/invitations", {
        method: "POST",
        body: {
          source,
          source_digest: data.source_digest,
          action,
          expected_version: data.linked_event?.version ?? 0,
          confirmed: true,
        },
      });
      if (!result.applied) throw new Error("REQUEST_FAILED");
      setCompleted(sourceKey);
      setConfirmed(false);
      await cache.invalidateQueries({ queryKey: ["mailbox-people-events"] });
      props.onCalendarChanged?.();
    } catch (reason) {
      if (
        reason instanceof ApiRequestError &&
        reason.code === "MAILBOX_SEND_UNCERTAIN"
      )
        setCompleted(sourceKey);
      setError(apiError(reason));
    } finally {
      setPending(false);
    }
  };
  if (
    invitation.error instanceof ApiRequestError &&
    invitation.error.code === "CALENDAR_INVITATION_NOT_FOUND"
  )
    return null;
  if (invitation.isError)
    return (
      <p role="alert" className="text-sm text-destructive">
        {apiError(invitation.error)}
      </p>
    );
  if (!data) return null;
  const formatDate = (value: string) =>
    new Intl.DateTimeFormat(i18n.language, {
      dateStyle: "medium",
      timeStyle: "short",
      timeZone: data.event?.timezone || "UTC",
    }).format(new Date(value));
  return (
    <section
      key={sourceKey}
      aria-label={t("invitation.title")}
      className="space-y-3 rounded-md border bg-muted/20 p-4 text-sm"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-medium">{t("invitation.title")}</h3>
        <Link className="underline underline-offset-4" to="/webmail/people">
          {t("people.title")}
        </Link>
      </div>
      <p className="break-words">
        {t("invitation.organizer", { address: data.organizer })}
      </p>
      <p
        className={
          data.verification === "verified"
            ? "text-muted-foreground"
            : "font-medium"
        }
      >
        {t(
          data.verification === "verified"
            ? "invitation.verified"
            : "invitation.unverified",
        )}
      </p>
      {data.event && (
        <div className="space-y-1">
          <p className="font-medium break-words">{data.event.summary}</p>
          <p>
            {formatDate(data.event.starts_at)} –{" "}
            {formatDate(data.event.ends_at)} ({data.event.timezone})
          </p>
          {data.event.location && (
            <p className="break-words">{data.event.location}</p>
          )}
        </div>
      )}
      {data.recurrence_id && <p>{t("invitation.single_occurrence")}</p>}
      {data.notification_status === "uncertain" && (
        <p role="alert">{t("invitation.uncertain")}</p>
      )}
      {error && (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      )}
      {completed === sourceKey ? (
        <p role="status">
          {t(error ? "invitation.uncertain" : "invitation.applied")}
        </p>
      ) : data.actions.length > 0 ? (
        <>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
              disabled={pending}
            />
            <span>{t("invitation.confirm")}</span>
          </label>
          <div className="flex flex-wrap gap-2">
            {data.actions.map((action) => (
              <Button
                key={action}
                size="sm"
                variant={
                  action === "decline" || action === "apply_cancel"
                    ? "outline"
                    : "default"
                }
                disabled={!confirmed || pending}
                onClick={() => void apply(action)}
              >
                {t(`invitation.action.${action}`)}
              </Button>
            ))}
          </div>
        </>
      ) : (
        data.notification_status !== "uncertain" && (
          <p>{t("invitation.no_action")}</p>
        )
      )}
    </section>
  );
}
