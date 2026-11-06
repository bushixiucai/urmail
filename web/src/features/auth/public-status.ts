import { useEffect, useState } from "react";
import { request } from "@/lib/api/client";

/** Public health is read-only. Registration is not a self-serve capability. */
export function usePublicStatus() {
  const [status, setStatus] = useState({
    quarantined: false,
    loaded: false,
  });
  useEffect(() => {
    const controller = new AbortController();
    void request<{ quarantined: boolean }>("/api/v1/healthz", {
      signal: controller.signal,
    })
      .then((health) => {
        if (!controller.signal.aborted)
          setStatus({
            quarantined: health.quarantined === true,
            loaded: true,
          });
      })
      .catch(() => {
        if (!controller.signal.aborted)
          setStatus({ quarantined: false, loaded: true });
      });
    return () => controller.abort();
  }, []);
  return status;
}
