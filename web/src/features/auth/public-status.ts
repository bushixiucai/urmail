import { useEffect, useState } from "react";
import { request } from "@/lib/api/client";

/** Public capabilities are read-only; missing evidence never enables registration. */
export function usePublicStatus() {
  const [status, setStatus] = useState({
    registration: false,
    quarantined: false,
    loaded: false,
  });
  useEffect(() => {
    const controller = new AbortController();
    void Promise.allSettled([
      request<{ enabled: boolean }>("/api/v1/auth/registration-status", {
        signal: controller.signal,
      }),
      request<{ quarantined: boolean }>("/api/v1/healthz", {
        signal: controller.signal,
      }),
    ])
      .then(([registration, health]) => {
        if (!controller.signal.aborted)
          setStatus({
            registration:
              registration.status === "fulfilled" &&
              registration.value.enabled === true &&
              health.status === "fulfilled" &&
              health.value.quarantined === false,
            quarantined:
              health.status === "fulfilled" &&
              health.value.quarantined === true,
            loaded: true,
          });
      })
      .catch(() => {});
    return () => controller.abort();
  }, []);
  return status;
}
