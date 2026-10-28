import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";

type Lifecycle = {
  host: { status: string; failure_reason?: string | null };
  certificate: {
    trusted: boolean;
    reason?: string;
    expires_at?: string;
  } | null;
};
type Challenge = { name: string; value: string; expires_at: string };

export function BrandHostLifecycle({
  hostname,
  onChange,
}: {
  hostname: string;
  onChange: () => void;
}) {
  const { t } = useTranslation();
  const apiError = useApiErrorMessage();
  const [state, setState] = useState<Lifecycle | null>(null);
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState("");
  const path = `/api/v1/platform/brand-hosts/${encodeURIComponent(hostname)}/lifecycle`;
  useEffect(() => {
    const controller = new AbortController();
    void request<Lifecycle>(path, { signal: controller.signal })
      .then((value) => {
        if (!controller.signal.aborted) setState(value);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(apiError(e));
      });
    return () => controller.abort();
  }, [path, apiError]);
  const execute = async (action: string) => {
    if (!confirm || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await request<Challenge>(path, {
        method: "POST",
        body: { action, confirm: true },
      });
      if (action === "challenge") setChallenge(result);
      setConfirm(false);
      setState(await request<Lifecycle>(path));
      onChange();
    } catch (e) {
      setError(apiError(e));
    } finally {
      setBusy(false);
    }
  };
  const refresh = async () => {
    setBusy(true);
    setError("");
    try {
      setState(await request<Lifecycle>(path));
    } catch (e) {
      setError(apiError(e));
    } finally {
      setBusy(false);
    }
  };
  const phase = state?.host.status;
  return (
    <section
      className="w-full space-y-2 rounded border p-3"
      aria-label={t("brand.host.lifecycle")}
    >
      <p>
        {t("brand.host.status")}:{" "}
        {phase ? t(`brand.host.phase_${phase}`) : t("common.loading")}
      </p>
      <p className="text-sm text-muted-foreground">{t("brand.host.scope")}</p>
      {state?.host.failure_reason && (
        <p className="text-sm">
          {t("brand.host.failure")}: {state.host.failure_reason}
        </p>
      )}
      {phase === "active" && (
        <p role="status">
          {t("brand.host.certificate")}:{" "}
          {t(
            state?.certificate?.trusted
              ? "brand.host.trusted"
              : "brand.host.not_ready",
          )}
        </p>
      )}
      {challenge && (
        <div className="space-y-1 text-sm">
          <p>{t("brand.host.publish")}</p>
          <pre className="overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-2">
            {challenge.name}
            {"\nTXT "}
            {challenge.value}
          </pre>
          <p>
            {t("brand.host.expires")}:{" "}
            {new Date(challenge.expires_at).toLocaleString()}
          </p>
        </div>
      )}
      {error && (
        <p role="alert" className="break-words text-sm text-destructive">
          {error}
        </p>
      )}
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={confirm}
          disabled={busy}
          onChange={(e) => setConfirm(e.target.checked)}
        />
        {t("brand.host.confirm")}
      </label>
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={() => void refresh()}
        >
          {t("brand.host.refresh")}
        </Button>
        {(phase === "pending-verify" || phase === "failed") && (
          <>
            <Button
              type="button"
              variant="outline"
              disabled={!confirm || busy}
              onClick={() => void execute("challenge")}
            >
              {t("brand.host.challenge")}
            </Button>
            <Button
              type="button"
              disabled={!confirm || busy}
              onClick={() => void execute("verify")}
            >
              {t("brand.host.verify")}
            </Button>
          </>
        )}
        {(phase === "verified" ||
          phase === "cert-issued" ||
          phase === "active") && (
          <Button
            type="button"
            disabled={!confirm || busy}
            onClick={() => void execute("activate")}
          >
            {t("brand.host.activate")}
          </Button>
        )}
      </div>
    </section>
  );
}
