import { FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  createContact,
  createEvent,
  listContacts,
  listEvents,
} from "./mailbox-api";
import { InvitationStatus, SendInvitationsButton } from "./mailbox-invitation";

export function MailboxPeoplePage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [summary, setSummary] = useState("");
  const contacts = useQuery({
    queryKey: ["mailbox-contacts"],
    queryFn: listContacts,
  });
  const start = new Date();
  const end = new Date(start.getTime() + 31 * 24 * 60 * 60 * 1000);
  const events = useQuery({
    queryKey: ["mailbox-events", start.toISOString().slice(0, 10)],
    queryFn: () => listEvents(start.toISOString(), end.toISOString()),
  });
  const addContact = useMutation({
    mutationFn: () => createContact(name, email),
    onSuccess: async () => {
      setName("");
      setEmail("");
      await queryClient.invalidateQueries({ queryKey: ["mailbox-contacts"] });
    },
  });
  const addEvent = useMutation({
    mutationFn: () => {
      const begins = new Date(Date.now() + 60 * 60 * 1000).toISOString();
      const finishes = new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString();
      return createEvent(summary, begins, finishes);
    },
    onSuccess: async () => {
      setSummary("");
      await queryClient.invalidateQueries({ queryKey: ["mailbox-events"] });
    },
  });

  function onContact(event: FormEvent) {
    event.preventDefault();
    addContact.mutate();
  }
  function onEvent(event: FormEvent) {
    event.preventDefault();
    addEvent.mutate();
  }

  return (
    <div className="mx-auto grid max-w-3xl gap-8 p-4">
      <section className="space-y-3">
        <h1 className="text-xl font-semibold">
          {t("page.webmail.contacts_title")}
        </h1>
        <form className="flex flex-wrap gap-2" onSubmit={onContact}>
          <Input
            aria-label={t("page.webmail.contact_name")}
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
          />
          <Input
            aria-label={t("page.webmail.contact_email")}
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
          <Button type="submit">{t("page.webmail.contact_add")}</Button>
        </form>
        <ul className="space-y-1">
          {(contacts.data?.contacts ?? []).map((contact) => (
            <li key={contact.id}>{contact.display_name}</li>
          ))}
        </ul>
      </section>
      <section className="space-y-3">
        <h2 className="text-xl font-semibold">
          {t("page.webmail.calendar_title")}
        </h2>
        <form className="flex flex-wrap gap-2" onSubmit={onEvent}>
          <Input
            aria-label={t("page.webmail.event_summary")}
            value={summary}
            onChange={(e) => setSummary(e.target.value)}
            required
          />
          <Button type="submit">{t("page.webmail.event_add")}</Button>
        </form>
        <ul className="space-y-2">
          {(events.data?.events ?? []).map((item) => (
            <li key={item.id} className="space-y-1">
              <div>{item.summary}</div>
              <InvitationStatus attendees={item.attendees} />
              {(item.attendees?.length ?? 0) > 0 && (
                <SendInvitationsButton eventId={item.id} />
              )}
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}
