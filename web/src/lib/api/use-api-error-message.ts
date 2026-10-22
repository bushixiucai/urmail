import { useTranslation } from "react-i18next";
import { getApiErrorKey } from "@/lib/api/client";

/**
 * Resolve any thrown error to a localised, user-visible message using the
 * shared i18n catalogue. Falls back to `error.REQUEST_FAILED` when neither
 * the error code nor the backend message has a matching key.
 */
export function useApiErrorMessage(fallbackKey = "error.REQUEST_FAILED") {
  const { t, i18n } = useTranslation();

  return (err: unknown): string => {
    const { key, params } = getApiErrorKey(err, fallbackKey);
    if (i18n.exists(key)) {
      return t(key, params);
    }
    return t(fallbackKey);
  };
}
