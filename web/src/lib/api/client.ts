export interface ApiError {
  error: {
    code: string;
    message?: string;
    params?: Record<string, unknown>;
    details?: unknown;
  };
}

/**
 * Hand-rolled guard for the API error envelope. Deliberately dependency-free
 * so that the whole zod runtime stays out of the first-paint bundle. Accepts
 * the same payloads the previous zod schema accepted: `error.code` must be a
 * string; `message` (string) and `params` (object) are optional; `details`
 * passes through unchecked; unknown keys are dropped.
 */
export function parseApiErrorPayload(body: unknown): ApiError | null {
  if (typeof body !== "object" || body === null) return null;
  const err = (body as { error?: unknown }).error;
  if (typeof err !== "object" || err === null) return null;
  const { code, message, params, details } = err as {
    code?: unknown;
    message?: unknown;
    params?: unknown;
    details?: unknown;
  };
  if (typeof code !== "string") return null;
  if (message !== undefined && typeof message !== "string") return null;
  if (params !== undefined && (typeof params !== "object" || params === null)) {
    return null;
  }
  return {
    error: {
      code,
      ...(message !== undefined ? { message } : {}),
      ...(params !== undefined
        ? { params: params as Record<string, unknown> }
        : {}),
      ...(details !== undefined ? { details } : {}),
    },
  };
}

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
    let parsed: ApiError | null = null;
    try {
      parsed = parseApiErrorPayload(await response.json());
    } catch {
      // Unparseable body falls through to the bounded INTERNAL_ERROR below.
    }
    throw new ApiRequestError(
      response.status,
      parsed ?? {
        error: { code: "INTERNAL_ERROR", message: "error.INTERNAL_ERROR" },
      },
    );
  }

  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}
