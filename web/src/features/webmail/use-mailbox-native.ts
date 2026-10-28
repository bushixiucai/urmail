import { useEffect, useState } from "react";
import { fetchMailboxStatus } from "./mailbox-api";

/** Whether URMAIL_WEBMAIL_NATIVE is on (GET /api/v1/mailbox/status). */
export function useMailboxNative() {
  const [native, setNative] = useState(false);
  const [mailHost, setMailHost] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    fetchMailboxStatus(controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          setNative(res.native === true);
          setMailHost(res.mail_host ?? "");
          setLoaded(true);
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setNative(false);
          setLoaded(true);
        }
      });
    return () => controller.abort();
  }, []);

  return { native, loaded, mailHost };
}
