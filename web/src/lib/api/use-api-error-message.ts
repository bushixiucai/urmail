import { useCallback } from "react";
import { useTranslation } from "react-i18next";
import { getApiErrorKey } from "@/lib/api/client";

/**
 * Resolve any thrown error to a localised, user-visible message using the
 * shared i18n catalogue. Falls back to `error.REQUEST_FAILED` when neither
 * the error code nor the backend message has a matching key.
 */
export function useApiErrorMessage(fallbackKey = "error.REQUEST_FAILED") {
  const { t, i18n } = useTranslation();

  return useCallback(
    (err: unknown): string => {
      const { key, params } = getApiErrorKey(err, fallbackKey);
      if (i18n.exists(key)) {
        return t(key, params);
      }
      if (
        err instanceof Error &&
        "messageKey" in err &&
        typeof err.messageKey === "string" &&
        i18n.exists(err.messageKey)
      ) {
        return t(err.messageKey, params);
      }
      return t(fallbackKey);
    },
    [t, i18n, fallbackKey],
  );
}
