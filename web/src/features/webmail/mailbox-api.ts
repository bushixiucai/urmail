import { ApiRequestError, apiErrorSchema } from "@/lib/api/client";

const MAILBOX_TOKEN_KEY = "urmail.mailbox-token";
const MAILBOX_ADDRESS_KEY = "urmail.mailbox-address";
const MAILBOX_EXPIRES_KEY = "urmail.mailbox-expires";

export interface MailboxFolder {
  name: string;
  name_enc: string;
  delimiter: string;
  flags?: string[];
}

export interface MailboxAddress {
  name?: string;
  email: string;
}

export interface MessageSummary {
  uid: number;
  subject: string;
  from: MailboxAddress[];
  date: string;
  flags: string[];
  size: number;
  /** ENVELOPE threading headers (populated when requested). */
  message_id?: string;
  in_reply_to?: string;
  /** Root UID of the message's thread and size of the loaded group. */
  thread_id?: number;
  thread_count?: number;
  /** Cross-folder search results are tagged with their source mailbox. */
  folder?: string;
  folder_enc?: string;
}

export type MailboxSortKey = "date" | "subject" | "from" | "size" | "";

export interface MailboxListResponse {
  messages: MessageSummary[];
  total: number;
  limit: number;
  offset: number;
  q?: string;
  /** Send back as `cursor` for the next stable page (0 = end). */
  next_cursor?: number;
  sort?: MailboxSortKey;
  server_sorted?: boolean;
  /** "server" (IMAP THREAD=REFERENCES) or "client" grouping. */
  thread_mode?: "" | "server" | "client";
}

export interface AttachmentMeta {
  part_id: string;
  filename: string;
  content_type: string;
  size: number;
  /** RFC 2392 Content-ID of inline parts (cid: rewriting). */
  content_id?: string;
}

export interface MessageDetail extends MessageSummary {
  uid_validity?: number;
  to?: MailboxAddress[];
  cc?: MailboxAddress[];
  reply_to?: MailboxAddress[];
  /** Present when resuming IMAP drafts that stored Bcc. */
  bcc?: MailboxAddress[];
  text?: string;
  html?: string;
  attachments: AttachmentMeta[];
  /** Render-time sanitization tokens (see server sanitize_html.go). */
  notes?: string[];
  /** More parts exist than the capped surfaced attachment list. */
  attachments_capped?: boolean;
}

export interface MailboxSession {
  token: string;
  expires_at: string;
  address: string;
}

export function getMailboxToken(): string | null {
  return window.sessionStorage.getItem(MAILBOX_TOKEN_KEY);
}

export function getMailboxAddress(): string | null {
  return window.sessionStorage.getItem(MAILBOX_ADDRESS_KEY);
}

export function setMailboxSession(session: MailboxSession): void {
  window.sessionStorage.setItem(MAILBOX_TOKEN_KEY, session.token);
  window.sessionStorage.setItem(MAILBOX_ADDRESS_KEY, session.address);
  window.sessionStorage.setItem(MAILBOX_EXPIRES_KEY, session.expires_at);
}

export function clearMailboxSession(): void {
  window.sessionStorage.removeItem(MAILBOX_TOKEN_KEY);
  window.sessionStorage.removeItem(MAILBOX_ADDRESS_KEY);
  window.sessionStorage.removeItem(MAILBOX_EXPIRES_KEY);
}

export function hasMailboxSession(): boolean {
  return Boolean(getMailboxToken());
}

interface MailboxRequestOptions {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  /** When false, do not send the mailbox bearer (login / status). */
  auth?: boolean;
  idempotencyKey?: string;
}

/** Mailbox API client — uses mailbox bearer, never the panel session token. */
export async function mailboxRequest<T>(
  path: string,
  options: MailboxRequestOptions = {},
): Promise<T> {
  const { method = "GET", body, signal, auth = true } = options;
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (options.idempotencyKey)
    headers["Idempotency-Key"] = options.idempotencyKey;
  if (auth) {
    const token = getMailboxToken();
    if (token) headers.Authorization = `Bearer ${token}`;
  }

  const response = await fetch(path, {
    method,
    headers,
    credentials: "same-origin",
    signal,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!response.ok) {
    let parsed;
    try {
      parsed = apiErrorSchema.parse(await response.json());
    } catch {
      throw new ApiRequestError(response.status, {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      });
    }
    throw new ApiRequestError(response.status, parsed);
  }

  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export function fetchMailboxStatus(signal?: AbortSignal) {
  return mailboxRequest<{ native: boolean }>("/api/v1/mailbox/status", {
    auth: false,
    signal,
  });
}

export function createMailboxSession(email: string, password: string) {
  return mailboxRequest<MailboxSession>("/api/v1/mailbox/session", {
    method: "POST",
    auth: false,
    body: { email, password },
  });
}

export function deleteMailboxSession() {
  return mailboxRequest<{ revoked: boolean }>("/api/v1/mailbox/session", {
    method: "DELETE",
  });
}

export function listFolders(signal?: AbortSignal) {
  return mailboxRequest<{ folders: MailboxFolder[] }>(
    "/api/v1/mailbox/folders",
    { signal },
  );
}

export function listMessages(
  nameEnc: string,
  opts: {
    limit?: number;
    offset?: number;
    q?: string;
    sort?: MailboxSortKey;
    order?: "asc" | "desc";
    /** UID anchor from next_cursor for stable pagination. */
    cursor?: number;
    thread?: boolean;
  } = {},
  signal?: AbortSignal,
) {
  const params = new URLSearchParams();
  if (opts.limit != null) params.set("limit", String(opts.limit));
  if (opts.offset != null) params.set("offset", String(opts.offset));
  if (opts.q != null && opts.q.trim() !== "") params.set("q", opts.q.trim());
  if (opts.sort) params.set("sort", opts.sort);
  if (opts.order) params.set("order", opts.order);
  if (opts.cursor) params.set("cursor", String(opts.cursor));
  if (opts.thread) params.set("thread", "1");
  const qs = params.toString();
  return mailboxRequest<MailboxListResponse>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages${qs ? `?${qs}` : ""}`,
    { signal },
  );
}

/**
 * Cross-folder search: bounded fan-out across the selected folders (default:
 * all top-level folders), merged by date. Results carry folder/folder_enc.
 */
export function crossFolderSearch(
  q: string,
  opts: { folders?: string[]; limit?: number } = {},
  signal?: AbortSignal,
) {
  const params = new URLSearchParams({ q: q.trim() });
  if (opts.folders?.length) params.set("folders", opts.folders.join(","));
  if (opts.limit != null) params.set("limit", String(opts.limit));
  return mailboxRequest<{
    messages: MessageSummary[];
    total: number;
    q: string;
  }>(`/api/v1/mailbox/search?${params}`, { signal });
}

export function getMessage(
  nameEnc: string,
  uid: number,
  opts?: { loadImages?: boolean; signal?: AbortSignal },
) {
  const params = new URLSearchParams();
  // Per-message "load images" privacy opt-in (WEBMAIL-007): default request
  // keeps remote images blocked server-side.
  if (opts?.loadImages) params.set("load_images", "1");
  const qs = params.toString();
  return mailboxRequest<MessageDetail>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}` +
      (qs ? "?" + qs : ""),
    { signal: opts?.signal },
  );
}

export function flagMessages(
  nameEnc: string,
  body: { uids: number[]; add?: string[]; remove?: string[] },
) {
  return mailboxRequest<{ updated: number }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/flags`,
    { method: "POST", body },
  );
}

export function moveMessages(
  nameEnc: string,
  body: { uids: number[]; to: string },
) {
  return mailboxRequest<{ moved: number; to: string }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/move`,
    { method: "POST", body },
  );
}

export function deleteMessages(
  nameEnc: string,
  body: { uids: number[]; expunge?: boolean },
) {
  return mailboxRequest<{ deleted: number; expunged: boolean }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/delete`,
    { method: "POST", body },
  );
}

export interface OutgoingAttachment {
  filename: string;
  content_type: string;
  content_base64: string;
}

export const MAX_SEND_ATTACHMENT_BYTES = 5 * 1024 * 1024;
export const MAX_SEND_TOTAL_ATTACHMENT_BYTES = 10 * 1024 * 1024;

/** Count decoded bytes without allocating another decoded attachment copy. */
export function attachmentDecodedBytes(attachment: OutgoingAttachment): number {
  const base64 = attachment.content_base64.replace(/\s/g, "");
  const padding = base64.endsWith("==") ? 2 : base64.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((base64.length * 3) / 4) - padding);
}

export function attachmentTotalBytes(
  attachments: OutgoingAttachment[],
): number {
  return attachments.reduce(
    (total, attachment) => total + attachmentDecodedBytes(attachment),
    0,
  );
}

/** Match the server's decoded aggregate limit before serializing JSON. */
export function assertAttachmentTotal(
  attachments: OutgoingAttachment[] = [],
): void {
  if (attachmentTotalBytes(attachments) > MAX_SEND_TOTAL_ATTACHMENT_BYTES) {
    throw new ApiRequestError(413, {
      error: {
        code: "MAILBOX_MESSAGE_TOO_LARGE",
        message: "error.MAILBOX_MESSAGE_TOO_LARGE",
      },
    });
  }
}
export const MAX_SEND_ATTACHMENTS = 10;

export async function sendMessage(body: {
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  text: string;
  html?: string;
  attachments?: OutgoingAttachment[];
}) {
  assertAttachmentTotal(body.attachments);
  return mailboxRequest<{ sent: boolean; sent_copy?: "saved" | "pending" }>(
    "/api/v1/mailbox/send",
    {
      method: "POST",
      body,
    },
  );
}

/** Save (or replace) a draft via IMAP APPEND into SPECIAL-USE \Drafts. */
export async function saveDraft(body: {
  uid?: number;
  uid_validity?: number;
  to?: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  text: string;
  html?: string;
  attachments?: OutgoingAttachment[];
}) {
  assertAttachmentTotal(body.attachments);
  return mailboxRequest<{
    saved: boolean;
    cleanup_pending?: boolean;
    uid: number;
    uid_validity: number;
    folder: string;
    name_enc: string;
  }>("/api/v1/mailbox/drafts", {
    method: "PUT",
    body,
  });
}

export function listDrafts(
  opts: { limit?: number; offset?: number } = {},
  signal?: AbortSignal,
) {
  const params = new URLSearchParams();
  if (opts.limit != null) params.set("limit", String(opts.limit));
  if (opts.offset != null) params.set("offset", String(opts.offset));
  const q = params.toString();
  return mailboxRequest<{
    folder: string;
    name_enc: string;
    messages: MessageSummary[];
    total: number;
    limit: number;
    offset: number;
  }>(`/api/v1/mailbox/drafts${q ? `?${q}` : ""}`, { signal });
}

export function deleteDraft(uid: number, uidValidity: number) {
  return mailboxRequest<{ deleted: boolean; uid: number }>(
    `/api/v1/mailbox/drafts/${uid}?uid_validity=${uidValidity}`,
    { method: "DELETE" },
  );
}

export interface ScheduledSendItem {
  id: number;
  draft_folder: string;
  draft_uid: number;
  draft_uidvalidity: number;
  send_at: string;
  next_attempt_at?: string;
  status: string;
  attempts: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

/** Retries must reuse the same key and exact draft reference. */
export interface ScheduleSendRequest {
  draft_uid: number;
  draft_uidvalidity: number;
  send_at: string;
}

export function scheduleSend(
  body: ScheduleSendRequest,
  idempotencyKey: string,
) {
  return mailboxRequest<ScheduledSendItem>("/api/v1/mailbox/scheduled-sends", {
    method: "POST",
    body,
    idempotencyKey,
  });
}

export function listScheduledSends(
  signal?: AbortSignal,
  opts: { limit?: number; offset?: number } = {},
) {
  const params = new URLSearchParams({
    limit: String(opts.limit ?? 50),
    offset: String(opts.offset ?? 0),
  });
  return mailboxRequest<{ items: ScheduledSendItem[] }>(
    `/api/v1/mailbox/scheduled-sends?${params}`,
    { signal },
  );
}

export function cancelScheduledSend(id: number) {
  return mailboxRequest<{ cancelled: boolean; item: ScheduledSendItem }>(
    `/api/v1/mailbox/scheduled-sends/${id}`,
    { method: "DELETE" },
  );
}

export function isDraftsFolder(
  folder: MailboxFolder | null | undefined,
): boolean {
  if (!folder) return false;
  const flags = folder.flags ?? [];
  if (flags.some((f) => f.toLowerCase() === "\\drafts")) return true;
  return folder.name.toLowerCase() === "drafts";
}

/** Fetch one attachment as base64 for resume-into-compose (same limits as send). */
export async function fetchAttachmentAsOutgoing(
  nameEnc: string,
  uid: number,
  partId: string,
  filename: string,
  contentType: string,
): Promise<OutgoingAttachment> {
  const token = getMailboxToken();
  const headers: Record<string, string> = { Accept: "*/*" };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}/attachments/${encodeURIComponent(partId)}`,
    { method: "GET", headers, credentials: "same-origin" },
  );
  if (!response.ok) {
    let parsed;
    try {
      parsed = apiErrorSchema.parse(await response.json());
    } catch {
      throw new ApiRequestError(response.status, {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      });
    }
    throw new ApiRequestError(response.status, parsed);
  }
  const buf = await response.arrayBuffer();
  if (buf.byteLength > MAX_SEND_ATTACHMENT_BYTES) {
    throw new Error("ATTACHMENT_TOO_LARGE");
  }
  const bytes = new Uint8Array(buf);
  const chunks: string[] = [];
  for (let i = 0; i < bytes.length; i += 0x8000) {
    chunks.push(String.fromCharCode(...bytes.subarray(i, i + 0x8000)));
  }
  return {
    filename: filename || `attachment-${partId}`,
    content_type: contentType || "application/octet-stream",
    content_base64: btoa(chunks.join("")),
  };
}

/** Fetch an attachment as a Blob using the mailbox bearer token. */
export async function downloadAttachment(
  nameEnc: string,
  uid: number,
  partId: string,
  filename: string,
): Promise<void> {
  const token = getMailboxToken();
  const headers: Record<string, string> = { Accept: "*/*" };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}/attachments/${encodeURIComponent(partId)}`,
    { method: "GET", headers, credentials: "same-origin" },
  );
  if (!response.ok) {
    let parsed;
    try {
      parsed = apiErrorSchema.parse(await response.json());
    } catch {
      throw new ApiRequestError(response.status, {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      });
    }
    throw new ApiRequestError(response.status, parsed);
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename || `attachment-${partId}`;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** @deprecated Prefer sanitizeHtmlMail from ./sanitize-html-mail. */
export { stripRemoteImages } from "./sanitize-html-mail";

/** Read a File into a sendMessage attachment payload (base64, no data-URL prefix). */
export function fileToOutgoingAttachment(
  file: File,
): Promise<OutgoingAttachment> {
  return new Promise((resolve, reject) => {
    if (file.size > MAX_SEND_ATTACHMENT_BYTES) {
      reject(new Error("ATTACHMENT_TOO_LARGE"));
      return;
    }
    const reader = new FileReader();
    reader.onerror = () => reject(new Error("ATTACHMENT_READ_FAILED"));
    reader.onload = () => {
      const result = String(reader.result ?? "");
      const comma = result.indexOf(",");
      const b64 = comma >= 0 ? result.slice(comma + 1) : result;
      if (!b64) {
        reject(new Error("ATTACHMENT_READ_FAILED"));
        return;
      }
      resolve({
        filename: file.name || "attachment",
        content_type: file.type || "application/octet-stream",
        content_base64: b64,
      });
    };
    reader.readAsDataURL(file);
  });
}

export function changeMailboxPassword(
  oldPassword: string,
  newPassword: string,
) {
  return mailboxRequest<{
    changed: boolean;
    reauthentication_required: boolean;
  }>("/api/v1/mailbox/password", {
    method: "POST",
    body: { old_password: oldPassword, new_password: newPassword },
  });
}

export function formatFrom(from: MailboxAddress[] | undefined): string {
  if (!from?.length) return "";
  const first = from[0];
  return first.name ? `${first.name} <${first.email}>` : first.email;
}

export function hasFlag(flags: string[] | undefined, flag: string): boolean {
  if (!flags) return false;
  const target = flag.toLowerCase();
  return flags.some((f) => f.toLowerCase() === target);
}

export interface MailboxContact {
  id: number;
  version: number;
  display_name: string;
  emails: { address: string; label?: string }[];
}

export function listContacts() {
  return mailboxRequest<{ contacts: MailboxContact[] }>(
    "/api/v1/mailbox/contacts",
  );
}

export function createContact(displayName: string, email: string) {
  return mailboxRequest<MailboxContact>("/api/v1/mailbox/contacts", {
    method: "POST",
    body: { display_name: displayName, emails: [{ address: email }] },
  });
}

export interface MailboxAttendee {
  email: string;
  /** iTIP PARTSTAT: needs-action (default), accepted, declined, tentative. */
  partstat: string;
}

export interface MailboxEvent {
  id: number;
  version: number;
  summary: string;
  starts_at: string;
  ends_at: string;
  attendees?: MailboxAttendee[];
}

export function listEvents(start: string, end: string) {
  const q = new URLSearchParams({ start, end });
  return mailboxRequest<{ events: MailboxEvent[] }>(
    "/api/v1/mailbox/calendars/events?" + q.toString(),
  );
}

export function createEvent(summary: string, startsAt: string, endsAt: string) {
  return mailboxRequest<MailboxEvent>("/api/v1/mailbox/calendars/events", {
    method: "POST",
    body: { summary, starts_at: startsAt, ends_at: endsAt, timezone: "UTC" },
  });
}
export async function sendEventInvitations(
  id: number,
  method: "request" | "cancel",
  confirm: boolean,
): Promise<{ queued: number; sequence: number; method: string }> {
  return mailboxRequest(
    "/api/v1/mailbox/calendars/events/" + id + "/invitations",
    {
      method: "POST",
      body: { confirm, method },
    },
  );
}

// WEBMAIL-005/006: mailbox-user self-service settings.

export interface MailboxSettingsView {
  display_name: string;
  signature_text: string;
  signature_html: string;
  preferences: Record<string, unknown>;
  version: number;
  updated_at: string;
  address: string;
}

export function getMailboxSettings() {
  return mailboxRequest<MailboxSettingsView>("/api/v1/mailbox/settings");
}

export function saveMailboxSettings(body: {
  display_name: string;
  signature_text: string;
  signature_html: string;
  preferences?: Record<string, unknown>;
  expected_version: number;
}) {
  return mailboxRequest<{ saved: boolean; version: number }>(
    "/api/v1/mailbox/settings",
    { method: "PUT", body },
  );
}

export interface MailboxIdentity {
  address: string;
  primary: boolean;
}

export function listMailboxIdentities() {
  return mailboxRequest<{ identities: MailboxIdentity[] }>(
    "/api/v1/mailbox/identities",
  );
}

export function getMailboxQuota() {
  return mailboxRequest<{ used_bytes: number; quota_bytes: number }>(
    "/api/v1/mailbox/quota",
  );
}

export interface MailboxSieveRule {
  header: "subject" | "from" | "to" | "cc";
  match: "contains" | "is";
  value: string;
  action: "fileinto" | "keep" | "discard";
  folder?: string;
}

export interface MailboxForwarding {
  enabled: boolean;
  target: string;
  keep_copy: boolean;
}

export interface MailboxVacation {
  enabled: boolean;
  subject: string;
  message: string;
  days: number;
}

export interface MailboxRulesView {
  rules: {
    rules: MailboxSieveRule[];
    forwarding?: MailboxForwarding;
    vacation: MailboxVacation;
  };
  version: string;
  state: string;
}

export function getMailboxRules() {
  return mailboxRequest<MailboxRulesView>("/api/v1/mailbox/rules");
}

export function saveMailboxRules(body: {
  rules: MailboxRulesView["rules"];
  confirmed: boolean;
  expected_version: string;
}) {
  return mailboxRequest<{ saved: boolean; state: string; version: string }>(
    "/api/v1/mailbox/rules",
    { method: "PUT", body },
  );
}
