import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export function ReminderPolicy() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [days, setDays] = useState("7,3,1,0");
  const [loaded, setLoaded] = useState(false);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    request<{ days: number[] }>("/api/v1/platform/reminder-policy", {
      signal: controller.signal,
    })
      .then((value) => {
        if (!controller.signal.aborted) {
          setDays(value.days.join(","));
          setLoaded(true);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setNotice(errorMessage(error));
          setLoaded(true);
        }
      });
    return () => controller.abort();
  }, [errorMessage]);
  return (
    <form
      className="space-y-3 rounded-md border p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (busy || !loaded) return;
        const values =
          days.trim() === ""
            ? []
            : days.split(",").map((value) => value.trim());
        if (
          values.length > 16 ||
          values.some(
            (value) => !/^\d{1,3}$/.test(String(value)) || Number(value) > 365,
          )
        ) {
          setNotice(t("notify.policy.invalid"));
          return;
        }
        setBusy(true);
        request<{ days: number[] }>("/api/v1/platform/reminder-policy", {
          method: "PUT",
          body: { days: values.map(Number) },
        })
          .then((value) => {
            setDays(value.days.join(","));
            setNotice(t("notify.policy.saved"));
          })
          .catch((error: unknown) => setNotice(errorMessage(error)))
          .finally(() => setBusy(false));
      }}
    >
      <label className="block space-y-1 text-sm">
        <span>{t("notify.policy.days")}</span>
        <Input
          value={days}
          onChange={(event) => setDays(event.target.value)}
          placeholder="7,3,1,0"
          disabled={!loaded || busy}
        />
      </label>
      <p className="text-sm text-muted-foreground">{t("notify.policy.help")}</p>
      {notice && <p role="status">{notice}</p>}
      <Button disabled={!loaded || busy} type="submit">
        {t("notify.policy.save")}
      </Button>
    </form>
  );
}
