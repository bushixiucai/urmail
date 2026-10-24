import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { request } from "@/lib/api/client";

export interface CloudPolicy {
  allowed: boolean;
  provider: string;
  model: string;
  endpoint: string;
  configuration_digest: string;
  scope_hash: string;
  requires_confirmation: boolean;
  tenant_local_only: boolean;
}

export function CloudConsent({
  policy,
  reload,
}: {
  policy: CloudPolicy | null;
  reload: () => void;
}) {
  const { t } = useTranslation();
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  useEffect(() => {
    setConfirmed(false);
    setNotice("");
  }, [policy?.configuration_digest, policy?.allowed]);
  if (!policy?.requires_confirmation) return null;
  const save = async (allow: boolean) => {
    setBusy(true);
    setNotice("");
    try {
      await request("/api/v1/ai/consent", {
        method: "POST",
        body: {
          configuration_digest: policy.configuration_digest,
          scope_hash: policy.scope_hash,
          allow_cloud: allow,
          confirmed: allow && confirmed,
        },
      });
      setConfirmed(false);
      reload();
      setNotice(t("page.ai.consent_saved"));
    } catch {
      setNotice(t("page.ai.consent_failed"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      className="space-y-3 rounded border p-4"
      aria-label={t("page.ai.consent_title")}
    >
      <h2 className="font-semibold">{t("page.ai.consent_title")}</h2>
      <p className="break-all text-sm">
        {policy.provider} · {policy.model} · {policy.endpoint}
      </p>
      <p className="text-sm">{t("page.ai.cloud_scope")}</p>
      <p role="status" className="text-sm">
        {t(
          policy.allowed
            ? "page.ai.consent_allowed"
            : "page.ai.consent_blocked",
        )}
      </p>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={confirmed}
          disabled={busy}
          onChange={(e) => setConfirmed(e.target.checked)}
        />
        {t("page.ai.consent_confirm_target")}
      </label>
      <div className="flex flex-wrap gap-2">
        <Button disabled={busy || !confirmed} onClick={() => void save(true)}>
          {t("page.ai.consent_authorize")}
        </Button>
        <Button
          variant="outline"
          disabled={busy || policy.tenant_local_only}
          onClick={() => void save(false)}
        >
          {t("page.ai.consent_revoke")}
        </Button>
      </div>
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
    </section>
  );
}
