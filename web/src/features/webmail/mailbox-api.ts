import type { AIBudgetStatus } from "@/features/ai/ai-budget";
export type { AIBudgetStatus };
import { ApiRequestError, parseApiErrorPayload } from "@/lib/api/client";
import {
  etagCacheKey,
  noteEtagResponse,
  revalidatingFetch,
} from "@/lib/api/client";
import { initViewportTracking, viewportHeaders } from "./viewport-headers";

// UA/viewport contract headers for server-side mail adaptation (PR #43).
// Installed once per module load; the resize listener is debounced.
if (typeof window !== "undefined") {
  initViewportTracking(window);
}

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

export interface MessageRef {
  folder_enc: string;
  uid_validity: number;
  uid: number;
}

export interface MessageSummary {
  uid: number;
  uid_validity?: number;
  has_attachments?: boolean;
  subject: string;
  /** One-line list preview when the server sends one. Not a copy of subject. */
  preview?: string;
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
  /** Degraded-contract marker (#119): the server could not verify this
   * candidate group; thread_id/thread_count are omitted for the row and
   * it must be rendered/handled as a single, independently-addressable
   * mail with a "conversation unverified" hint. */
  thread_verification?: "unverified";
  /** Cross-folder search results are tagged with their source mailbox. */
  folder?: string;
  folder_enc?: string;
}

export type MailboxSortKey = "date" | "subject" | "from" | "size" | "";

export interface MailboxListResponse {
  uid_validity?: number;
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
  inline?: boolean;
  part_id: string;
  filename: string;
  content_type: string;
  size: number;
  /** RFC 2392 Content-ID of inline parts (cid: rewriting). */
  content_id?: string;
}

export interface MessageDetail extends MessageSummary {
  authentication?: {
    status: "pass" | "fail" | "unknown";
    source?: string;
    reason: string;
    domain?: string;
    checked_at?: string;
  };
  security_notes?: string[];
  risk_level?: "low" | "medium" | "high" | "critical";
  indicators?: MailboxAIIndicator[];
  /**
   * Persisted phishing_v2 verdict (Backend #87, internal/webmail/types.go
   * PhishingFinding): present only after an analysis ran, key omitted for
   * unanalyzed mail so the client stays silent. `score >= 0.6` renders the
   * high-risk banner; anything below stays silent.
   */
  phishing?: MailboxPhishingFinding;
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

function getMailboxStorageItem(key: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    return (
      window.localStorage.getItem(key) ?? window.sessionStorage.getItem(key)
    );
  } catch {
    return null;
  }
}

export function getMailboxToken(): string | null {
  return getMailboxStorageItem(MAILBOX_TOKEN_KEY);
}

export function getMailboxAddress(): string | null {
  return getMailboxStorageItem(MAILBOX_ADDRESS_KEY);
}

export function getMailboxExpiresAt(): string | null {
  return getMailboxStorageItem(MAILBOX_EXPIRES_KEY);
}

export function setMailboxSession(
  session: MailboxSession,
  remember = true,
): void {
  if (typeof window === "undefined") return;
  // A new session re-arms the 401 redirect funnel after any prior expiry.
  redirectingToLogin = false;
  try {
    const target = remember ? window.localStorage : window.sessionStorage;
    const clean = remember ? window.sessionStorage : window.localStorage;

    target.setItem(MAILBOX_TOKEN_KEY, session.token);
    target.setItem(MAILBOX_ADDRESS_KEY, session.address);
    target.setItem(MAILBOX_EXPIRES_KEY, session.expires_at);

    clean.removeItem(MAILBOX_TOKEN_KEY);
    clean.removeItem(MAILBOX_ADDRESS_KEY);
    clean.removeItem(MAILBOX_EXPIRES_KEY);
  } catch {
    // Ignore storage errors
  }
}

export function clearMailboxSession(): void {
  if (typeof window === "undefined") return;
  try {
    for (const key of [
      MAILBOX_TOKEN_KEY,
      MAILBOX_ADDRESS_KEY,
      MAILBOX_EXPIRES_KEY,
    ]) {
      window.localStorage.removeItem(key);
      window.sessionStorage.removeItem(key);
    }
  } catch {
    // Ignore storage errors
  }
}

/**
 * True only when a session exists AND its expires_at is still in the future.
 * A dead token is cleared here so pages fail fast to the login screen instead
 * of firing a burst of doomed requests.
 */
export function hasMailboxSession(): boolean {
  if (!getMailboxToken()) return false;
  const expiresAt = getMailboxExpiresAt();
  if (!expiresAt) return true;
  const expiresMs = Date.parse(expiresAt);
  if (Number.isNaN(expiresMs)) return true;
  if (expiresMs <= Date.now()) {
    clearMailboxSession();
    return false;
  }
  return true;
}

/**
 * Session expiry redirect must fire once per dead session: concurrent 401s
 * (folders + settings + quotes…) otherwise each trigger their own navigate.
 * `window.location.assign` survives React trees that never mounted a Router.
 * A fresh login re-arms the funnel. The indirection exists so tests can
 * observe navigations (jsdom's Location is unforgeable).
 */
let redirectingToLogin = false;
let navigateToLogin: (target: string) => void = (target) => {
  window.location.assign(target);
};
/** @internal test seam — swap the navigation sink for redirect assertions. */
export function setMailboxLoginRedirectHandler(
  handler: ((target: string) => void) | null,
): void {
  navigateToLogin = handler ?? ((target) => window.location.assign(target));
}
function redirectToLoginOn401(): void {
  if (redirectingToLogin) return;
  redirectingToLogin = true;
  clearMailboxSession();
  if (typeof window === "undefined") return;
  const current = `${window.location.pathname}${window.location.search}`;
  const target = `/webmail/login`;
  if (current === target) {
    redirectingToLogin = false;
    return;
  }
  navigateToLogin(target);
}

function isMailboxAuthError(status: number, code?: string): boolean {
  return (
    status === 401 ||
    code === "MAILBOX_AUTHORIZATION_DENIED" ||
    code === "MAILBOX_REAUTH_REQUIRED"
  );
}

interface MailboxRequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
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
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...viewportHeaders(),
  };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (options.idempotencyKey)
    headers["Idempotency-Key"] = options.idempotencyKey;
  if (auth) {
    const token = getMailboxToken();
    if (token) headers.Authorization = `Bearer ${token}`;
  }

  const cacheKey = etagCacheKey(method, path, getMailboxToken());
  const { response, fromCache, payload } = await revalidatingFetch<T>(
    path,
    {
      method,
      headers,
      credentials: "same-origin",
      signal,
      body: body === undefined ? undefined : JSON.stringify(body),
    },
    method,
    cacheKey,
  );

  if (fromCache) return payload as T;

  if (!response.ok) {
    let parsed;
    try {
      parsed = parseApiErrorPayload(await response.json()) ?? {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    } catch {
      parsed = {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    }
    if (
      auth !== false &&
      isMailboxAuthError(response.status, parsed.error?.code)
    ) {
      // Single funnel: one dead session = one redirect, whatever raced.
      redirectToLoginOn401();
    }
    throw new ApiRequestError(response.status, parsed);
  }

  if (response.status === 204) return undefined as T;
  const data = (await response.json()) as T;
  noteEtagResponse(cacheKey, response, data);
  return data;
}

export function fetchMailboxStatus(signal?: AbortSignal) {
  return mailboxRequest<{ native: boolean; mail_host?: string }>(
    "/api/v1/mailbox/status",
    {
      auth: false,
      signal,
    },
  );
}

/** `remember` also drives the backend session TTL (long-lived when true). */
export function createMailboxSession(
  email: string,
  password: string,
  remember = false,
) {
  return mailboxRequest<MailboxSession>("/api/v1/mailbox/session", {
    method: "POST",
    auth: false,
    body: { email, password, remember },
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
  opts?: { loadImages?: boolean; signal?: AbortSignal; uidValidity?: number },
) {
  const params = new URLSearchParams();
  // Per-message "load images" privacy opt-in (WEBMAIL-007): default request
  // keeps remote images blocked server-side.
  if (opts?.loadImages) params.set("load_images", "1");
  if (opts?.uidValidity) params.set("uid_validity", String(opts.uidValidity));
  const qs = params.toString();
  return mailboxRequest<MessageDetail>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}` +
      (qs ? "?" + qs : ""),
    { signal: opts?.signal },
  );
}

export function flagMessages(
  nameEnc: string,
  body: {
    uids: number[];
    add?: string[];
    remove?: string[];
    uid_validity?: number;
  },
) {
  return mailboxRequest<{ updated: number }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/flags`,
    { method: "POST", body },
  );
}

export function moveMessages(
  nameEnc: string,
  body: { uids: number[]; to: string; uid_validity?: number },
) {
  return mailboxRequest<{
    moved: number;
    to: string;
    destination_uids?: number[];
    /** UIDVALIDITY of the destination box for the COPYUIDed uids (undo). */
    destination_uid_validity?: number;
  }>(`/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/move`, {
    method: "POST",
    body,
  });
}

export function deleteMessages(
  nameEnc: string,
  body: { uids: number[]; expunge?: boolean; uid_validity?: number },
) {
  return mailboxRequest<{ deleted: number; expunged: boolean }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/delete`,
    { method: "POST", body },
  );
}

export interface OutgoingAttachment {
  content_id?: string;
  inline?: boolean;
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

export interface SendMessageRequest {
  from?: string;
  reply_to?: string;
  in_reply_to?: string;
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  text: string;
  html?: string;
  attachments?: OutgoingAttachment[];
  draft_source?: MessageRef;
  acknowledged_uncertain_receipt_id?: number;
  request_mdn?: boolean;
  request_dsn?: boolean;
}

export interface SendReceipt {
  draft_released?: boolean;
  id: number;
  request_key: string;
  status: "prepared" | "submitting" | "accepted" | "rejected" | "uncertain";
  sent_copy_state: "none" | "pending" | "unknown" | "complete" | "held";
  message_id: string;
  created_at: string;
  accepted_at?: string;
  last_error?: string;
  draft_source?: MessageRef;
}

export async function sendMessage(
  body: SendMessageRequest,
  requestKey: string = crypto.randomUUID(),
) {
  assertAttachmentTotal(body.attachments);
  return mailboxRequest<{
    sent: boolean;
    sent_copy?: "saved" | "pending";
    receipt?: SendReceipt;
  }>("/api/v1/mailbox/send", {
    method: "POST",
    body,
    idempotencyKey: requestKey,
  });
}

export async function getSendReceiptByRequestKey(
  key: string,
): Promise<SendReceipt | null> {
  const response = await mailboxRequest<{ items: SendReceipt[] }>(
    `/api/v1/mailbox/send-receipts?request_key=${encodeURIComponent(key)}`,
  );
  return response.items[0] ?? null;
}

export function listSendReceipts(
  options: {
    limit?: number;
    offset?: number;
    request_key?: string;
  } & Partial<MessageRef> = {},
  signal?: AbortSignal,
) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(options)) {
    if (value !== undefined) params.set(key, String(value));
  }
  return mailboxRequest<{ items: SendReceipt[] }>(
    `/api/v1/mailbox/send-receipts?${params}`,
    { signal },
  );
}

export async function getSendReceiptForDraft(
  source: MessageRef,
  signal?: AbortSignal,
): Promise<SendReceipt | null> {
  const response = await listSendReceipts(source, signal);
  return response.items[0] ?? null;
}

export function repairSentCopy(id: number) {
  return mailboxRequest<{ receipt: SendReceipt }>(
    `/api/v1/mailbox/send-receipts/${id}/repair-sent-copy`,
    { method: "POST" },
  );
}

/** Save (or replace) a draft via IMAP APPEND into SPECIAL-USE \Drafts. */
export async function saveDraft(
  body: {
    from?: string;
    in_reply_to?: string;
    reply_source?: MailboxAISource;
    uid?: number;
    uid_validity?: number;
    to?: string[];
    cc?: string[];
    bcc?: string[];
    subject: string;
    text: string;
    html?: string;
    attachments?: OutgoingAttachment[];
  },
  requestKey: string = crypto.randomUUID(),
) {
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
    idempotencyKey: requestKey,
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
  sent_copy_state?: "none" | "pending" | "unknown" | "complete" | "held";
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
  from?: string;
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
  uidValidity?: number,
  signal?: AbortSignal,
): Promise<OutgoingAttachment> {
  const token = getMailboxToken();
  const headers: Record<string, string> = {
    Accept: "*/*",
    ...viewportHeaders(),
  };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}/attachments/${encodeURIComponent(partId)}${uidValidity ? `?uid_validity=${uidValidity}` : ""}`,
    { method: "GET", headers, credentials: "same-origin", signal },
  );
  if (!response.ok) {
    let parsed;
    try {
      parsed = parseApiErrorPayload(await response.json()) ?? {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    } catch {
      parsed = {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    }
    if (isMailboxAuthError(response.status, parsed.error?.code))
      redirectToLoginOn401();
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
  uidValidity?: number,
): Promise<void> {
  const token = getMailboxToken();
  const headers: Record<string, string> = {
    Accept: "*/*",
    ...viewportHeaders(),
  };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}/attachments/${encodeURIComponent(partId)}${uidValidity ? `?uid_validity=${uidValidity}` : ""}`,
    { method: "GET", headers, credentials: "same-origin" },
  );
  if (!response.ok) {
    let parsed;
    try {
      parsed = parseApiErrorPayload(await response.json()) ?? {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    } catch {
      parsed = {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    }
    if (isMailboxAuthError(response.status, parsed.error?.code))
      redirectToLoginOn401();
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

export interface MailboxIdentityProfile {
  display_name: string;
  signature_text: string;
  signature_html: string;
}

export interface MailboxPreferences {
  read_timer_mode?: "5s" | "immediate" | "manual";
  after_delete_action?: "next" | "list";
  density?: "comfortable" | "compact" | "list";
  preview_layout?: "right" | "bottom" | "none";
  visible_columns?: string[];
  allow_all_remote_images?: boolean;
  trusted_image_senders?: string[];
  notifications_enabled?: boolean;
  notification_sound_enabled?: boolean;
  auto_bcc_enabled?: boolean;
  auto_bcc_address?: string;
  reply_to_address?: string;
  ai?: MailboxUserAIConfig | Record<string, unknown>;
  [key: string]: unknown;
}

export interface MailboxSettingsView {
  identity_profiles?: Record<string, MailboxIdentityProfile>;
  display_name: string;
  signature_text: string;
  signature_html: string;
  preferences: MailboxPreferences;
  version: number;
  updated_at: string;
  address: string;
}

export function getMailboxSettings() {
  return mailboxRequest<MailboxSettingsView>("/api/v1/mailbox/settings");
}

export const MAILBOX_AI_CONFIGURATION_CHANGED =
  "urmail.mailbox-ai-configuration-changed";

export async function saveMailboxSettings(body: {
  identity_profiles?: Record<string, MailboxIdentityProfile>;
  display_name: string;
  signature_text: string;
  signature_html: string;
  preferences?: Record<string, unknown>;
  expected_version: number;
}) {
  const result = await mailboxRequest<{ saved: boolean; version: number }>(
    "/api/v1/mailbox/settings",
    { method: "PUT", body },
  );
  if (
    typeof window !== "undefined" &&
    body.preferences &&
    Object.prototype.hasOwnProperty.call(body.preferences, "ai")
  ) {
    // No credentials or configuration data travel with this invalidation signal.
    window.dispatchEvent(new Event(MAILBOX_AI_CONFIGURATION_CHANGED));
  }
  return result;
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
  return mailboxRequest<{
    used_bytes: number;
    quota_bytes: number;
    max_message_bytes?: number;
  }>("/api/v1/mailbox/quota");
}

export interface RuleCondition {
  header: "subject" | "from" | "to" | "cc";
  match: "contains" | "is";
  value: string;
}

export interface MailboxSieveRule {
  disabled?: boolean;
  header: "subject" | "from" | "to" | "cc";
  match: "contains" | "is";
  value: string;
  conditions?: RuleCondition[];
  match_type?: "all" | "any";
  action: "fileinto" | "keep" | "discard";
  folder?: string;
}

export interface MailboxForwarding {
  enabled: boolean;
  target: string;
  keep_copy: boolean;
}

export interface MailboxVacation {
  from?: string;
  until?: string;
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

export interface MailboxUserAIConfig {
  use_custom?: boolean;
  provider?: "openai" | "ollama" | "azure";
  endpoint?: string;
  model?: string;
  api_key?: string;
  key_configured?: boolean;
  clear_api_key?: boolean;
}

export interface MailboxAITarget {
  enabled: boolean;
  allowed: boolean;
  is_custom?: boolean;
  provider?: string;
  model?: string;
  endpoint?: string;
  configuration_digest?: string;
  scope_hash?: string;
  requires_confirmation?: boolean;
}
export interface MailboxAISource {
  folder_enc: string;
  uid: number;
  uid_validity: number;
}
export type MailboxAIOperation =
  | "classify"
  | "summarize"
  | "translate"
  | "phishing"
  | "polish"
  | "draft_new"
  | "draft_reply"
  | "todos";
export interface MailboxAITodo {
  task: string;
  assignee?: string | null;
  due_date?: string | null;
}

export interface MailboxAIKeyDate {
  date: string;
  label: string;
}

export interface MailboxAIChange {
  type: "clarity" | "grammar" | "tone" | "structure";
  note: string;
}

export interface MailboxAIIndicator {
  type:
    | "spoofed_domain"
    | "suspicious_link"
    | "urgent_pressure"
    | "credential_request"
    | "other";
  evidence?: string;
}

/** Persisted phishing_v2 verdict on the message API (Backend #87). */
export interface MailboxPhishingFinding {
  /** 0..1; `>= 0.6` renders the high-risk banner. */
  score?: number;
  risk_level?: "low" | "medium" | "high" | "critical";
  reasons?: string[];
  indicators?: MailboxAIIndicator[];
  /** Settlement time of the latest analysis (UTC). */
  analyzed_at?: string;
}

export interface MailboxAIResult {
  // Legacy compatibility fields
  category?: string;
  confidence?: number;
  /** Phishing score (0..1); `>= 0.6` means high risk. */
  score?: number;
  summary?: string;
  translation?: string;
  draft?: string;
  subject?: string;
  body?: string;
  reasons?: string[];
  text?: string;

  // Structured v2 fields
  bullets?: string[];
  todos?: MailboxAITodo[];
  key_dates?: MailboxAIKeyDate[];
  language?: string;

  tone_notes?: string;
  assumptions?: string[];

  revised_text?: string;
  changes?: MailboxAIChange[];
  tone?: "neutral" | "formal" | "concise" | "friendly";

  translated_text?: string;
  detected_source?: string;

  risk_level?: "low" | "medium" | "high" | "critical";
  indicators?: MailboxAIIndicator[];

  items?: MailboxAITodo[];
  task_type?: string;
  result?: string;
}

export interface MailboxAIAssistResponse {
  result: MailboxAIResult;
  degraded?: boolean;
  degrade_reason?: "provider_unavailable" | "schema_retry_exhausted" | "";
  template_version?: number;
  cache_hit?: boolean;
}

export interface MailboxAICitation extends MailboxAISource {
  id: string;
  quote: string;
}
export const fetchMailboxAITarget = (signal?: AbortSignal) =>
  mailboxRequest<MailboxAITarget>("/api/v1/mailbox/ai/target", { signal });
export function fetchMailboxAIContext(
  source: MailboxAISource,
  signal?: AbortSignal,
) {
  const params = new URLSearchParams({
    folder_enc: source.folder_enc,
    uid: String(source.uid),
    uid_validity: String(source.uid_validity),
  });
  return mailboxRequest<{ subject: string; body: string; truncated: boolean }>(
    `/api/v1/mailbox/ai/context?${params}`,
    { signal },
  );
}
export function runMailboxAI(
  body: {
    operation: MailboxAIOperation;
    configuration_digest: string;
    confirmed: boolean;
    content_digest?: string;
    request_key: string;
    body?: string;
    subject?: string;
    instruction?: string;
    selection?: string;
    /**
     * The draft text under review. Required by the server for polish without a
     * message source (compose drafts): body carries the surrounding draft and
     * this field carries the exact text to refine.
     */
    draft?: string;
    target_language?: string;
    length_limit?: number;
  } & Partial<MailboxAISource>,
  signal?: AbortSignal,
) {
  return mailboxRequest<MailboxAIAssistResponse>("/api/v1/mailbox/ai/assist", {
    method: "POST",
    body,
    signal,
  });
}
export function askMailboxAI(
  body: {
    question: string;
    configuration_digest: string;
    confirmed: boolean;
    content_digest?: string;
    folder_enc?: string;
    after?: string;
    before?: string;
  },
  signal?: AbortSignal,
) {
  return mailboxRequest<{
    text: string;
    grounded: boolean;
    citations: string[];
  }>("/api/v1/mailbox/ai/search-ask", { method: "POST", body, signal });
}

export interface MailboxAIPreview {
  content_digest: string;
  provider?: string;
  model?: string;
  endpoint?: string;
  messages?: Array<{ role: string; content: string }>;
  question?: string;
  sources?: Array<{ ID: string; Quote: string }>;
}

export function previewMailboxAI(
  kind: "assist" | "search-ask",
  body: object,
  signal?: AbortSignal,
) {
  return mailboxRequest<MailboxAIPreview>(`/api/v1/mailbox/ai/${kind}`, {
    method: "POST",
    body: { ...body, confirmed: false, preview_only: true },
    signal,
  });
}
export const fetchMailboxAICitation = (id: string, signal?: AbortSignal) =>
  mailboxRequest<MailboxAICitation>(
    `/api/v1/mailbox/ai/citations/${encodeURIComponent(id)}`,
    { signal },
  );
export const fetchMailboxAIBudget = (signal?: AbortSignal) =>
  mailboxRequest<AIBudgetStatus>("/api/v1/mailbox/ai/budget", { signal });

export function fetchMailboxAIModels(
  body?: {
    provider?: string;
    endpoint?: string;
    model?: string;
    api_key?: string;
  },
  signal?: AbortSignal,
) {
  return mailboxRequest<{ status: string; models: string[] }>(
    "/api/v1/mailbox/ai/models",
    { method: "POST", body: body ?? {}, signal },
  );
}

export interface MailboxMutationResult {
  results: Array<{
    uid: number;
    status: "success" | "failed" | "unknown";
    code?: string;
    destination_uid?: number;
  }>;
  destination?: string;
  name_enc?: string;
  uid_validity?: number;
  atomic?: boolean;
}
export function createMailboxFolder(name: string) {
  return mailboxRequest<{
    /** Legacy alias kept for existing dialog consumers. */
    folder?: MailboxFolder;
    name?: string;
    name_enc?: string;
    uid_validity?: number;
  }>("/api/v1/mailbox/folders", { method: "POST", body: { name } });
}
export function renameMailboxFolder(
  nameEnc: string,
  name: string,
  uidValidity: number,
) {
  return mailboxRequest<{
    /** Legacy alias kept for existing dialog consumers. */
    folder?: MailboxFolder;
    name?: string;
    name_enc?: string;
    destination?: string;
    deleted?: boolean;
  }>(`/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}`, {
    method: "PATCH",
    body: { name, uid_validity: uidValidity },
  });
}
export function deleteMailboxFolder(nameEnc: string, uidValidity: number) {
  return mailboxRequest<{
    name?: string;
    name_enc?: string;
    destination?: string;
    deleted: boolean;
    recoverable?: boolean;
  }>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}?uid_validity=${uidValidity}`,
    { method: "DELETE" },
  );
}
export async function mailboxTransfer(
  nameEnc: string,
  action: "copy" | "trash" | "restore",
  body: { uids: number[]; to?: string; uid_validity: number },
) {
  // Keep the exact intent key through transport errors and page reloads. A
  // confirmed completed action releases it, allowing a deliberate second copy.
  const slot =
    "urmail.mail-transfer." +
    JSON.stringify([
      getMailboxAddress(),
      nameEnc,
      action,
      { ...body, uids: [...body.uids].sort((a, b) => a - b) },
    ]);
  const existing = sessionStorage.getItem(slot);
  const key = existing ?? crypto.randomUUID();
  if (!existing) sessionStorage.setItem(slot, key);
  const result = await mailboxRequest<MailboxMutationResult>(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${action}`,
    { method: "POST", body, idempotencyKey: key },
  );
  if (result.results.every((item) => item.status !== "unknown"))
    sessionStorage.removeItem(slot);
  return result;
}
export function isTrashFolder(
  folder: MailboxFolder | null | undefined,
): boolean {
  return (
    !!folder &&
    (folder.name.toLowerCase() === "trash" ||
      (folder.flags ?? []).some((flag) => flag.toLowerCase() === "\\trash"))
  );
}

/** Return exact original MIME as one bounded message/rfc822 attachment. */
export async function fetchMessageAsOutgoing(
  nameEnc: string,
  uid: number,
  uidValidity: number,
  signal?: AbortSignal,
): Promise<OutgoingAttachment> {
  const token = getMailboxToken();
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(nameEnc)}/messages/${uid}/raw?uid_validity=${uidValidity}`,
    {
      headers: {
        Authorization: `Bearer ${token ?? ""}`,
        ...viewportHeaders(),
      },
      credentials: "same-origin",
      cache: "no-store",
      signal,
    },
  );
  if (!response.ok) {
    let parsed;
    try {
      parsed = parseApiErrorPayload(await response.json()) ?? {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    } catch {
      parsed = {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      };
    }
    if (isMailboxAuthError(response.status, parsed.error?.code))
      redirectToLoginOn401();
    throw new ApiRequestError(response.status, parsed);
  }
  if (!response.body) throw Error("ORIGINAL_MESSAGE_UNAVAILABLE");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.length;
      if (total > MAX_SEND_ATTACHMENT_BYTES)
        throw Error("ATTACHMENT_TOO_LARGE");
      chunks.push(value);
    }
  } finally {
    await reader.cancel().catch(() => {});
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  const pieces: string[] = [];
  for (let index = 0; index < bytes.length; index += 0x8000)
    pieces.push(String.fromCharCode(...bytes.subarray(index, index + 0x8000)));
  return {
    filename: `message-${uid}.eml`,
    content_type: "message/rfc822",
    content_base64: btoa(pieces.join("")),
  };
}

export function repairScheduledSentCopy(id: number) {
  return mailboxRequest<{ item: ScheduledSendItem }>(
    `/api/v1/mailbox/scheduled-sends/${id}/repair-sent-copy`,
    { method: "POST" },
  );
}

export function acknowledgeUncertainScheduledSend(id: number) {
  return mailboxRequest<ScheduledSendItem>(
    `/api/v1/mailbox/scheduled-sends/${id}/acknowledge-uncertain`,
    { method: "POST", body: { confirmed: true } },
  );
}

// --- WP05 & I16: Personal Canned Snippets ---
export interface MailboxSnippet {
  id: number;
  title: string;
  content: string;
  shortcut?: string;
  created_at: string;
  updated_at: string;
}

export function listMailboxSnippets() {
  return mailboxRequest<{ snippets: MailboxSnippet[] }>(
    "/api/v1/mailbox/snippets",
  );
}

export function createMailboxSnippet(body: {
  title: string;
  content: string;
  shortcut?: string;
}) {
  return mailboxRequest<MailboxSnippet>("/api/v1/mailbox/snippets", {
    method: "POST",
    body,
  });
}

export function updateMailboxSnippet(
  id: number,
  body: { title: string; content: string; shortcut?: string },
) {
  return mailboxRequest<MailboxSnippet>(`/api/v1/mailbox/snippets/${id}`, {
    method: "PUT",
    body,
  });
}

export function deleteMailboxSnippet(id: number) {
  return mailboxRequest<{ deleted: boolean }>(
    `/api/v1/mailbox/snippets/${id}`,
    {
      method: "DELETE",
    },
  );
}

// --- WP06 & I13: Corporate Directory ---
export interface CorporateContact {
  id: number;
  display_name: string;
  given_name?: string;
  family_name?: string;
  organization?: string;
  department?: string;
  job_title?: string;
  emails: { address: string; type?: string; primary?: boolean }[];
  phones: { number: string; type?: string; primary?: boolean }[];
  addresses?: unknown[];
  note?: string;
}

export function listCorporateContacts() {
  return mailboxRequest<{ contacts: CorporateContact[]; enabled: boolean }>(
    "/api/v1/mailbox/corporate-directory",
  );
}

export function searchCorporateContacts(query: string) {
  return mailboxRequest<{ contacts: CorporateContact[] }>(
    `/api/v1/mailbox/corporate-directory/search?q=${encodeURIComponent(query)}`,
  );
}

// --- WP07 & I12: Sender Allow / Deny Rules ---
export interface SenderRule {
  scope: "mailbox" | "tenant";
  address: string;
  domain: string;
  list: "allow" | "deny";
}

export function listSenderRules() {
  return mailboxRequest<{ rules: SenderRule[] }>(
    "/api/v1/mailbox/spam/senders",
  );
}

export function addSenderRule(body: {
  address?: string;
  domain?: string;
  list: "allow" | "deny";
}) {
  return mailboxRequest<{ saved: boolean }>("/api/v1/mailbox/spam/senders", {
    method: "POST",
    body,
  });
}

export function deleteSenderRule(body: {
  address?: string;
  domain?: string;
  list: "allow" | "deny";
}) {
  return mailboxRequest<{ deleted: boolean }>("/api/v1/mailbox/spam/senders", {
    method: "DELETE",
    body,
  });
}
