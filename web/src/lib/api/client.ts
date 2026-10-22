import { z } from "zod";

export const apiErrorSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    params: z.record(z.string(), z.unknown()).optional(),
    details: z.unknown().optional(),
  }),
});

export type ApiError = z.infer<typeof apiErrorSchema>;

export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly i18nKey: string;
  readonly params: Record<string, unknown>;

  constructor(status: number, body: ApiError) {
    super(body.error.message || body.error.code);
    this.name = "ApiRequestError";
    this.status = status;
    this.code = body.error.code;
    this.i18nKey = body.error.message;
    this.params = body.error.params ?? {};
  }
}

export function getApiErrorMessage(
  err: unknown,
  fallback = "操作失败，请重试",
): string {
  if (err instanceof ApiRequestError) {
    return err.i18nKey || err.code || fallback;
  }
  if (err instanceof Error) {
    return err.message;
  }
  return fallback;
}

const SESSION_TOKEN_KEY = "urmail.session-token";
const CSRF_TOKEN_KEY = "urmail.csrf-token";

export function getSessionToken(): string | null {
  return window.sessionStorage.getItem(SESSION_TOKEN_KEY);
}

export function setSessionToken(token: string): void {
  window.sessionStorage.setItem(SESSION_TOKEN_KEY, token);
}

export function getCSRFToken(): string | null {
  return window.sessionStorage.getItem(CSRF_TOKEN_KEY);
}

export function setCSRFToken(csrf: string): void {
  window.sessionStorage.setItem(CSRF_TOKEN_KEY, csrf);
}

export function clearAuth(): void {
  window.sessionStorage.removeItem(SESSION_TOKEN_KEY);
  window.sessionStorage.removeItem(CSRF_TOKEN_KEY);
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PATCH" | "PUT" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
}

export async function request<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { method = "GET", body, signal } = options;

  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";

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
