import { ApiRequestError, parseApiErrorPayload } from "@/lib/api/client";
import { getMailboxToken, mailboxRequest } from "./mailbox-api";

export const MAX_MAIL_IMPORT_BYTES = 20 * 1024 * 1024;
export type MailImportStatus =
  | "ready"
  | "duplicate"
  | "invalid"
  | "pending"
  | "importing"
  | "imported"
  | "unknown"
  | "failed"
  | "skipped"
  | "canceled";
export interface MailImportItem {
  index: number;
  subject: string;
  message_id: string;
  size: number;
  sha256: string;
  status: MailImportStatus;
  error_code?: string;
  duplicate_of?: number;
  duplicate_uids?: number[];
  reconcile_absent?: boolean;
  reconciled_at?: string;
}
export interface MailImportJob {
  id: number;
  version: number;
  status: string;
  format: "eml" | "mbox";
  filename: string;
  folder_enc: string;
  uid_validity: number;
  source_digest: string;
  preview_digest: string;
  expires_at: string;
  payload_expires_at?: string;
  total: number;
  bytes: number;
  duplicate_policy: "skip" | "keep_both";
  items: MailImportItem[];
  counts?: Partial<Record<MailImportStatus, number>>;
  quota?: { used_bytes: number; quota_bytes: number };
}
export function previewMailImport(
  body: {
    format: "eml" | "mbox";
    filename: string;
    content_base64: string;
    folder_enc: string;
    uid_validity: number;
    duplicate_policy: "skip" | "keep_both";
  },
  key: string,
  signal?: AbortSignal,
) {
  return mailboxRequest<{ job: MailImportJob }>(
    "/api/v1/mailbox/imports/preview",
    { method: "POST", body, idempotencyKey: key, signal },
  );
}
export function listMailImports(signal?: AbortSignal) {
  return mailboxRequest<{ jobs: MailImportJob[] }>("/api/v1/mailbox/imports", {
    signal,
  });
}
export function getMailImport(id: number, signal?: AbortSignal) {
  return mailboxRequest<{ job: MailImportJob }>(
    `/api/v1/mailbox/imports/${id}`,
    { signal },
  );
}
export function mutateMailImport(
  id: number,
  action: "confirm" | "resume" | "reconcile" | "retry" | "cancel",
  body: Record<string, unknown>,
  signal?: AbortSignal,
) {
  return mailboxRequest<{ job: MailImportJob }>(
    `/api/v1/mailbox/imports/${id}/${action}`,
    { method: "POST", body, signal },
  );
}

// Stream and bound the authenticated export; never buffer an unbounded mailbox.
export async function downloadMailboxMbox(
  folder: string,
  validity: number,
  uids: number[],
  signal?: AbortSignal,
) {
  const params = new URLSearchParams({
    uid_validity: String(validity),
    uids: uids.join(","),
  });
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(folder)}/export.mbox?${params}`,
    {
      headers: { Authorization: `Bearer ${getMailboxToken() ?? ""}` },
      credentials: "same-origin",
      redirect: "error",
      cache: "no-store",
      signal,
    },
  );
  if (!response.ok) {
    let code = "MAIL_IMPORT_EXPORT_FAILED";
    try {
      code = parseApiErrorPayload(await response.json())?.error.code ?? code;
    } catch {
      /* Keep a bounded public failure code. */
    }
    throw new ApiRequestError(response.status, {
      error: { code, message: `error.${code}` },
    });
  }
  const media = response.headers
    .get("Content-Type")
    ?.split(";", 1)[0]
    .trim()
    .toLowerCase();
  if (media !== "application/mbox" && media !== "application/octet-stream")
    throw new Error("export content type unavailable");
  if (!response.body) throw new Error("export response unavailable");
  const reader = response.body.getReader();
  const parts: BlobPart[] = [];
  let bytes = 0;
  try {
    for (;;) {
      const result = await reader.read();
      if (result.done) break;
      bytes += result.value.byteLength;
      if (bytes > MAX_MAIL_IMPORT_BYTES) {
        try {
          await reader.cancel();
        } catch {
          /* Size rejection remains authoritative. */
        }
        throw new ApiRequestError(413, {
          error: {
            code: "MAIL_IMPORT_EXPORT_TOO_LARGE",
            message: "error.MAIL_IMPORT_EXPORT_TOO_LARGE",
          },
        });
      }
      parts.push(result.value.slice().buffer);
    }
  } finally {
    reader.releaseLock();
  }
  if (bytes === 0) throw new Error("empty export");
  const url = URL.createObjectURL(
    new Blob(parts, { type: "application/mbox" }),
  );
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "mailbox-export.mbox";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
