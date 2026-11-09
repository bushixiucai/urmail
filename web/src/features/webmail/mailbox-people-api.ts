import { ApiRequestError, parseApiErrorPayload } from "@/lib/api/client";
import {
  getMailboxToken,
  mailboxRequest,
  type MailboxAttendee,
} from "./mailbox-api";

export interface ContactAddress {
  street?: string;
  city?: string;
  region?: string;
  postal_code?: string;
  country?: string;
  label?: string;
}

export interface PeopleContact {
  id: number;
  addressbook_id: number;
  version: number;
  display_name: string;
  given_name: string;
  family_name: string;
  emails: { address: string; label?: string }[];
  phones: { number: string; label?: string }[];
  note: string;
  organization?: string;
  job_title?: string;
  addresses?: ContactAddress[];
  birthday?: string;
  photo?: string;
  vcard_extra?: Record<string, unknown>;
}
export type ContactDraft = Pick<
  PeopleContact,
  "display_name" | "given_name" | "family_name" | "emails" | "phones" | "note"
> & {
  organization?: string;
  job_title?: string;
  addresses?: ContactAddress[];
  birthday?: string;
  photo?: string;
  vcard_extra?: Record<string, unknown>;
};
export interface PeopleGroup {
  id: number;
  addressbook_id: number;
  name: string;
}
export interface OccurrenceOverride {
  recurrence_id: string;
  summary: string;
  description: string;
  location: string;
  starts_at: string;
  ends_at: string;
  all_day: boolean;
  status: string;
}
export interface PeopleEvent {
  id: number;
  version: number;
  uid: string;
  summary: string;
  description: string;
  location: string;
  starts_at: string;
  ends_at: string;
  all_day: boolean;
  timezone: string;
  rrule: string;
  status: string;
  reminders?: number[];
  occurrences: string[];
  instances: OccurrenceOverride[];
  attendees: MailboxAttendee[];
  recurrence_overrides: OccurrenceOverride[] | null;
}
export type EventDraft = Pick<
  PeopleEvent,
  | "summary"
  | "description"
  | "location"
  | "starts_at"
  | "ends_at"
  | "all_day"
  | "timezone"
  | "rrule"
  | "status"
> & {
  attendees: string[];
  reminders?: number[];
  recurrence_id?: string;
  recurrence_overrides?: OccurrenceOverride[];
};
const root = "/api/v1/mailbox";
export const peopleContacts = (cursor = 0) =>
  mailboxRequest<{ contacts: PeopleContact[]; next_cursor: number }>(
    `${root}/contacts?cursor=${cursor}`,
  );
export const savePeopleContact = (
  draft: ContactDraft,
  existing?: PeopleContact,
) =>
  mailboxRequest<PeopleContact>(
    `${root}/contacts${existing ? "/" + existing.id : ""}`,
    {
      method: existing ? "PUT" : "POST",
      body: { ...draft, ...(existing ? { version: existing.version } : {}) },
    },
  );
export const deletePeopleContact = (contact: PeopleContact) =>
  mailboxRequest(`${root}/contacts/${contact.id}?version=${contact.version}`, {
    method: "DELETE",
  });
export const peopleGroups = () =>
  mailboxRequest<{ groups: PeopleGroup[] }>(`${root}/contacts/groups`);
export const createPeopleGroup = (name: string) =>
  mailboxRequest(`${root}/contacts/groups`, { method: "POST", body: { name } });
export const deletePeopleGroup = (id: number) =>
  mailboxRequest(`${root}/contacts/groups/${id}`, { method: "DELETE" });
export const peopleGroupMembers = (id: number) =>
  mailboxRequest<{ contacts: PeopleContact[] }>(
    `${root}/contacts/groups/${id}/members`,
  );
export const addPeopleGroupMember = (
  group: PeopleGroup,
  contact: PeopleContact,
) =>
  mailboxRequest(`${root}/contacts/groups/members`, {
    method: "POST",
    body: {
      group_id: group.id,
      contact_id: contact.id,
      group_addressbook_id: group.addressbook_id,
      contact_addressbook_id: contact.addressbook_id,
    },
  });
export const removePeopleGroupMember = (
  group: PeopleGroup,
  contact: PeopleContact,
) =>
  mailboxRequest(`${root}/contacts/groups/${group.id}/members/${contact.id}`, {
    method: "DELETE",
  });
export const peopleEvents = (start: string, end: string, cursor = 0) =>
  mailboxRequest<{ events: PeopleEvent[]; next_cursor: number }>(
    `${root}/calendars/events?${new URLSearchParams({ start, end, cursor: String(cursor) })}`,
  );
export const savePeopleEvent = (draft: EventDraft, existing?: PeopleEvent) =>
  mailboxRequest(
    `${root}/calendars/events${existing ? "/" + existing.id : ""}`,
    {
      method: existing ? "PUT" : "POST",
      body: { ...draft, ...(existing ? { version: existing.version } : {}) },
    },
  );
export const deletePeopleEvent = (
  event: PeopleEvent,
  recurrence?: string,
  notify = false,
) =>
  mailboxRequest(
    `${root}/calendars/events/${event.id}?${new URLSearchParams({ version: String(event.version), ...(recurrence ? { recurrence_id: recurrence } : {}), ...(notify ? { notify: "attendees" } : {}) })}`,
    { method: "DELETE" },
  );

export interface MetadataImportInput {
  kind: "contacts" | "calendar";
  format: "vcf" | "ics" | "csv";
  content: string;
  mode: "skip" | "keep_both";
  digest?: string;
}
export interface MetadataImportResult {
  digest: string;
  valid: number;
  added: number;
  skipped: number;
  errors: { index: number; key: string }[];
  names?: string[];
  committed: boolean;
}
export const previewPeopleImport = (
  body: MetadataImportInput,
  signal?: AbortSignal,
) =>
  mailboxRequest<MetadataImportResult>(`${root}/data/import/preview`, {
    method: "POST",
    body,
    signal,
  });
export const commitPeopleImport = (
  body: MetadataImportInput,
  key: string,
  signal?: AbortSignal,
) =>
  mailboxRequest<MetadataImportResult>(`${root}/data/import/commit`, {
    method: "POST",
    body,
    idempotencyKey: key,
    signal,
  });
export const peopleImportHistory = () =>
  mailboxRequest<{
    imports: {
      id: number;
      kind: string;
      idempotency_key: string;
      result: MetadataImportResult;
    }[];
  }>(`${root}/data/imports`);

export async function downloadPeopleFile(path: string, filename: string) {
  const response = await fetch(root + path, {
    headers: { Authorization: "Bearer " + (getMailboxToken() ?? "") },
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) {
    const parsed = parseApiErrorPayload(
      await response.json().catch(() => null),
    );
    throw new ApiRequestError(
      response.status,
      parsed ?? {
        error: { code: "REQUEST_FAILED", message: "error.REQUEST_FAILED" },
      },
    );
  }
  const objectUrl = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = objectUrl;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(objectUrl), 1000);
}

export interface MailboxReminder {
  id: number;
  payload: {
    event_id: number;
    summary: string;
    starts_at: string;
    ends_at: string;
    timezone: string;
    recurrence_id: string;
    minutes_before: number;
    fire_at: string;
    acknowledged: boolean;
  };
  created_at: string;
}
export const mailboxReminders = () =>
  mailboxRequest<{ reminders: MailboxReminder[] }>(
    `${root}/calendars/reminders`,
  );
export const acknowledgeMailboxReminder = (id: number) =>
  mailboxRequest<{ acknowledged: boolean }>(
    `${root}/calendars/reminders/${id}/ack`,
    {
      method: "POST",
      body: {},
    },
  );
