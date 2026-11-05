// web/src/features/webmail-next/feature-flag.ts

const WEBMAIL_VERSION_KEY = "urmail.webmail_version";

export function isWebmailNextEnabled(
  preferences?: Record<string, unknown> | null,
): boolean {
  if (typeof window === "undefined") return false;

  try {
    const params = new URLSearchParams(window.location.search);
    const queryV = params.get("v") || params.get("webmail_version");
    if (queryV === "next") return true;
    if (queryV === "legacy") return false;

    if (preferences?.webmail_version === "next") return true;
    if (preferences?.webmail_version === "legacy") return false;

    const stored =
      window.localStorage.getItem(WEBMAIL_VERSION_KEY) ??
      window.sessionStorage.getItem(WEBMAIL_VERSION_KEY);
    if (stored === "next") return true;
    if (stored === "legacy") return false;
  } catch {
    // ignore storage or url parse error
  }

  return false;
}

export function setWebmailNextEnabled(enabled: boolean): void {
  if (typeof window === "undefined") return;
  try {
    if (enabled) {
      window.localStorage.setItem(WEBMAIL_VERSION_KEY, "next");
    } else {
      window.localStorage.setItem(WEBMAIL_VERSION_KEY, "legacy");
    }
  } catch {
    // ignore
  }
}
