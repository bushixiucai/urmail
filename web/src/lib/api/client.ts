import { z } from "zod";

export const apiErrorSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string().optional(),
    params: z.record(z.string(), z.unknown()).optional(),
    details: z.unknown().optional(),
  }),
});

export type ApiError = z.infer<typeof apiErrorSchema>;

/** A message that looks like an i18n catalogue key, e.g. "error.CONFLICT". */
const I18N_KEY_PATTERN = /^[a-z][a-zA-Z0-9]*(\.[a-zA-Z0-9_-]+)+$/;

export function looksLikeI18nKey(value: string): boolean {
  return I18N_KEY_PATTERN.test(value);
}

export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: string;
  /** The backend-provided message, which doubles as an i18n catalogue key. */
  readonly messageKey: string;
  readonly params: Record<string, unknown>;

  constructor(status: number, body: ApiError) {
    super(body.error.message || body.error.code);
    this.name = "ApiRequestError";
    this.status = status;
    this.code = body.error.code;
    this.messageKey = body.error.message ?? "";
    this.params = body.error.params ?? {};
  }
}

export interface ApiErrorMessage {
  /** i18n key to render, possibly with interpolation params. */
  key: string;
  params: Record<string, unknown>;
}

/**
 * Map a thrown error to an i18n key so that every user-visible message is
 * localised. Resolution order for API errors:
 *   1. `error.<CODE>` from the catalogue's error.* series;
 *   2. the backend `message` when it is itself a catalogue key;
 *   3. the caller's fallback key.
 */
export function getApiErrorKey(
  err: unknown,
  fallbackKey = "error.REQUEST_FAILED",
): ApiErrorMessage {
  if (err instanceof ApiRequestError) {
    const codeKey = `error.${err.code}`;
    if (looksLikeI18nKey(codeKey)) {
      return { key: codeKey, params: err.params };
    }
    if (err.messageKey && looksLikeI18nKey(err.messageKey)) {
      return { key: err.messageKey, params: err.params };
    }
    return { key: fallbackKey, params: {} };
  }
  return { key: fallbackKey, params: {} };
}

const SESSION_TOKEN_KEY = "urmail.session-token";
const CSRF_TOKEN_KEY = "urmail.csrf-token";
const MAILBOX_TOKEN_KEY = "urmail.mailbox-token";

function getStorageItem(key: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    return (
      window.localStorage.getItem(key) ?? window.sessionStorage.getItem(key)
    );
  } catch {
    return null;
  }
}

export function getSessionToken(): string | null {
  return getStorageItem(SESSION_TOKEN_KEY);
}

export function setSessionToken(token: string, remember = true): void {
  if (typeof window === "undefined") return;
  try {
    if (remember) {
      window.localStorage.setItem(SESSION_TOKEN_KEY, token);
      window.sessionStorage.removeItem(SESSION_TOKEN_KEY);
    } else {
      window.sessionStorage.setItem(SESSION_TOKEN_KEY, token);
      window.localStorage.removeItem(SESSION_TOKEN_KEY);
    }
  } catch {
    // Ignore storage errors
  }
}

export function getCSRFToken(): string | null {
  return getStorageItem(CSRF_TOKEN_KEY);
}

export function setCSRFToken(csrf: string, remember = true): void {
  if (typeof window === "undefined") return;
  try {
    if (remember) {
      window.localStorage.setItem(CSRF_TOKEN_KEY, csrf);
      window.sessionStorage.removeItem(CSRF_TOKEN_KEY);
    } else {
      window.sessionStorage.setItem(CSRF_TOKEN_KEY, csrf);
      window.localStorage.removeItem(CSRF_TOKEN_KEY);
    }
  } catch {
    // Ignore storage errors
  }
}

export function clearAuth(): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(SESSION_TOKEN_KEY);
    window.localStorage.removeItem(CSRF_TOKEN_KEY);
    window.localStorage.removeItem(MAILBOX_TOKEN_KEY);
    window.sessionStorage.removeItem(SESSION_TOKEN_KEY);
    window.sessionStorage.removeItem(CSRF_TOKEN_KEY);
    window.sessionStorage.removeItem(MAILBOX_TOKEN_KEY);
  } catch {
    // Ignore storage errors
  }
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PATCH" | "PUT" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  idempotencyKey?: string;
}

/** Download a protected PDF without putting a session token in its URL. */
export async function downloadInvoice(id: number): Promise<void> {
  const token = getSessionToken();
  const response = await fetch(`/api/v1/billing/invoices/${id}/pdf`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    credentials: "same-origin",
  });
  if (!response.ok) {
    const body = apiErrorSchema.safeParse(
      await response.json().catch(() => null),
    );
    throw new ApiRequestError(
      response.status,
      body.success
        ? body.data
        : {
            error: { code: "REQUEST_FAILED" },
          },
    );
  }
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = `invoice-${id}.pdf`;
  link.click();
  URL.revokeObjectURL(url);
}

export function checkoutDestination(value: string): string {
  const url = new URL(value);
  if (
    url.protocol !== "https:" ||
    url.hostname !== "checkout.stripe.com" ||
    url.username ||
    url.password
  ) {
    throw new ApiRequestError(502, {
      error: { code: "PAYMENT_PROVIDER_ERROR" },
    });
  }
  return url.href;
}

export async function request<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { method = "GET", body, signal, idempotencyKey } = options;

  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;

  const token = getSessionToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  const csrf = getCSRFToken();
  if (csrf && method !== "GET") {
    headers["X-CSRF-Token"] = csrf;
  }

  const response = await fetch(path, {
    method,
    headers,
    credentials: "same-origin",
    signal,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!response.ok) {
    let parsed: ApiError;
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
