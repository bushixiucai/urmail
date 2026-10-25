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
}

export interface AttachmentMeta {
  part_id: string;
  filename: string;
  content_type: string;
  size: number;
}

export interface MessageDetail extends MessageSummary {
  text?: string;
  html?: string;
  attachments: AttachmentMeta[];
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
  method?: "GET" | "POST" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  /** When false, do not send the mailbox bearer (login / status). */
  auth?: boolean;
}

/** Mailbox API client — uses mailbox bearer, never the panel session token. */
export async function mailboxRequest<T>(
  path: string,
  options: MailboxRequestOptions = {},
): Promise<T> {
  const { method = "GET", body, signal, auth = true } = options;
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
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
  opts: { limit?: number; offset?: number } = {},
  signal?: AbortSignal,
) {
  const params = new URLSearchParams();
  if (opts.limit != null) params.set("limit", String(opts.limit));
  if (opts.offset != null) params.set("offset", String(opts.offset));
  const q = params.toString();
  return mailboxRequest<{
    messages: MessageSummary[];
    total: number;
    limit: number;
    offset: number;
  }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages${q ? `?${q}` : ""}`,
    { signal },
  );
}

export function getMessage(
  nameEnc: string,
  uid: number,
  signal?: AbortSignal,
) {
  return mailboxRequest<MessageDetail>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}`,
    { signal },
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

export function sendMessage(body: {
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  text: string;
  html?: string;
}) {
  return mailboxRequest<{ sent: boolean }>("/api/v1/mailbox/send", {
    method: "POST",
    body,
  });
}

export function changeMailboxPassword(oldPassword: string, newPassword: string) {
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
