import { z } from "zod";

/**
 * The API error envelope.
 *
 * The backend returns a machine-readable code, an i18n key and the parameters
 * needed to render it. The frontend never receives a pre-localised string, so
 * a message can be rendered in the reader's language regardless of the
 * server's locale.
 */
export const apiErrorSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    params: z.record(z.string(), z.unknown()).optional(),
    details: z.unknown().optional(),
  }),
});

export type ApiError = z.infer<typeof apiErrorSchema>;

/** Thrown for any non-2xx response. */
export class ApiRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly params: Record<string, unknown>;

  constructor(status: number, body: ApiError) {
    super(`${body.error.code}`);
    this.name = "ApiRequestError";
    this.status = status;
    this.code = body.error.code;
    this.params = body.error.params ?? {};
  }
}

/**
 * The session token is kept in sessionStorage rather than localStorage so
 * that it does not outlive the browser session. Combined with the CSP that
 * forbids inline scripts, this keeps a stored token from being reachable by
 * an injected script.
 */
const SESSION_TOKEN_KEY = "urmail.session-token";

export function getSessionToken(): string | null {
  return window.sessionStorage.getItem(SESSION_TOKEN_KEY);
}

export function setSessionToken(token: string): void {
  window.sessionStorage.setItem(SESSION_TOKEN_KEY, token);
}

export function clearSessionToken(): void {
  window.sessionStorage.removeItem(SESSION_TOKEN_KEY);
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PATCH" | "PUT" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
}

/** Perform an authenticated API request. */
export async function request<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { method = "GET", body, signal } = options;

  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const token = getSessionToken();
  if (token) headers.Authorization = `Bearer ${token}`;

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
