import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { getApiErrorKey } from "@/lib/api/client";
import { sendEventInvitations, type MailboxAttendee } from "./mailbox-api";

const STATUS_ORDER = [
  "needs-action",
  "accepted",
  "tentative",
  "declined",
] as const;

export function invitationStatusLabel(partstat: string | undefined): string {
  const status = STATUS_ORDER.includes(
    (partstat ?? "needs-action") as (typeof STATUS_ORDER)[number],
  )
    ? (partstat ?? "needs-action")
    : "needs-action";
  return "page.webmail.invitation_status_" + status;
}

/** InvitationStatus shows each attendee's stored PARTSTAT answer. */
export function InvitationStatus({
  attendees,
}: {
  attendees: MailboxAttendee[] | undefined;
}) {
  const { t } = useTranslation();
  const list = attendees ?? [];
  if (list.length === 0) return null;
  return (
    <ul
      className="ml-4 list-disc text-sm text-muted-foreground"
      aria-label={t("page.webmail.invitation_attendees")}
    >
      {list.map((attendee) => (
        <li key={attendee.email}>
          {attendee.email}: {t(invitationStatusLabel(attendee.partstat))}
        </li>
      ))}
    </ul>
  );
}

/**
 * SendInvitationsButton confirms the invitation burst for one event. The
 * backend refuses without this explicit confirm flag and rate limits one
 * burst per event version, so the button reports either the queued count or
 * the i18n-visible refusal.
 */
export function SendInvitationsButton({
  eventId,
  method = "request",
}: {
  eventId: number;
  method?: "request" | "cancel";
}) {
  const { t } = useTranslation();
  const [result, setResult] = useState<string | null>(null);
  const send = useMutation({
    mutationFn: () => sendEventInvitations(eventId, method, true),
    onSuccess: (res) => {
      setResult(t("page.webmail.invitation_sent", { count: res.queued }));
    },
    onError: (err: unknown) => {
      setResult(t(getApiErrorKey(err).key, getApiErrorKey(err).params));
    },
  });
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Button
        type="button"
        variant="outline"
        aria-label={t("page.webmail.invitation_send")}
        disabled={send.isPending}
        onClick={() => send.mutate()}
      >
        {method === "cancel"
          ? t("page.webmail.invitation_cancel")
          : t("page.webmail.invitation_send")}
      </Button>
      {result && (
        <span role="status" className="text-sm text-muted-foreground">
          {result}
        </span>
      )}
    </span>
  );
}
