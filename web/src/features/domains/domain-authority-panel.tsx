import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Domain = { id: number; fqdn: string; mode: string; status: string };
type Authority = {
  domain: string;
  phase: string;
  enabled: boolean;
  ready: boolean;
  local_signatures_valid: boolean;
  parent_ds_checked: boolean;
  parent_ds_matches: boolean;
  chain_valid: boolean;
  ds: string[];
  parent_ds: string[];
  signatures_expire_at?: string;
  rollover_not_before?: string;
  can_disable: boolean;
  secondary_soa_matches?: boolean;
  secondary?: {
    hostname: string;
    address: string;
    primary_address: string;
    tsig_key_name: string;
  };
};
export function DomainAuthorityPanel({ domain }: { domain: Domain }) {
  const { t } = useTranslation();
  const apiError = useApiErrorMessage();
  const [state, setState] = useState<Authority | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [manifest, setManifest] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [action, setAction] = useState("prepare");
  const [secondary, setSecondary] = useState({
    hostname: "",
    address: "",
    primary_address: "",
    tsig_key_name: "",
  });
  const path = `/api/v1/domains/${domain.id}/authority`;
  useEffect(() => {
    const controller = new AbortController();
    setState(null);
    setError("");
    setNotice("");
    setManifest("");
    setAction("prepare");
    setSecondary({
      hostname: "",
      address: "",
      primary_address: "",
      tsig_key_name: "",
    });
    setConfirm(false);
    if (domain.mode === "managed" && domain.status === "active")
      void request<Authority>(path, { signal: controller.signal })
        .then((value) => {
          if (!controller.signal.aborted) setState(value);
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(apiError(e));
        });
    return () => controller.abort();
  }, [path, domain.mode, domain.status, apiError]);
  if (domain.mode !== "managed") return null;
  const refresh = async () => {
    setBusy(true);
    setError("");
    try {
      setState(await request<Authority>(path));
    } catch (e) {
      setError(apiError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      className="space-y-3 rounded-md border p-4"
      aria-labelledby={`authority-${domain.id}`}
    >
      <h3 id={`authority-${domain.id}`} className="font-medium">
        {t("domain.authority.title")}
      </h3>
      <p className="text-sm text-muted-foreground">
        {t("domain.authority.scope")}
      </p>
      {error && (
        <p role="alert" className="break-words text-sm text-destructive">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      <Button
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => void refresh()}
      >
        {t("domain.authority.refresh")}
      </Button>
      {state && (
        <div className="space-y-2 text-sm">
          <p>
            {t("domain.authority.phase")}:{" "}
            {t(`domain.authority.phase_${state.phase}`)}
          </p>
          <ul className="space-y-1">
            {(
              [
                "local_signatures_valid",
                "parent_ds_checked",
                "parent_ds_matches",
                "chain_valid",
                "ready",
              ] as const
            ).map((key) => (
              <li key={key}>
                {t(`domain.authority.${key}`)}:{" "}
                {t(state[key] ? "domain.authority.yes" : "domain.authority.no")}
              </li>
            ))}
          </ul>
          {state.ds?.length > 0 && (
            <div>
              <p>{t("domain.authority.ds")}</p>
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-2 text-xs">
                {state.ds.join("\n")}
              </pre>
            </div>
          )}
          {state.secondary && (
            <p>
              {t("domain.authority.secondary_soa_matches")}:{" "}
              {t(
                state.secondary_soa_matches
                  ? "domain.authority.yes"
                  : "domain.authority.no",
              )}
            </p>
          )}
          {state.rollover_not_before &&
            !state.rollover_not_before.startsWith("0001-") && (
              <p>
                {t("domain.authority.rollover_not_before")}:{" "}
                {new Date(state.rollover_not_before).toLocaleString()}
              </p>
            )}
          {state.signatures_expire_at &&
            !state.signatures_expire_at.startsWith("0001-") && (
              <p>
                {t("domain.authority.signatures_expire_at")}:{" "}
                {new Date(state.signatures_expire_at).toLocaleString()}
              </p>
            )}
          <p className="text-xs text-muted-foreground">
            {t("domain.authority.secondary_boundary")}
          </p>
        </div>
      )}
      {manifest && (
        <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-3 text-xs">
          {manifest}
        </pre>
      )}
      <form
        className="grid gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (!confirm || busy) return;
          setBusy(true);
          setError("");
          setNotice("");
          setManifest("");
          void request(path, {
            method: "POST",
            body: {
              action,
              confirm: true,
              ...(action === "secondary-configure" ? { secondary } : {}),
            },
          })
            .then(async (result) => {
              if (action === "backup-manifest")
                setManifest(JSON.stringify(result, null, 2));
              setConfirm(false);
              setNotice(t("domain.authority.changed"));
              await refresh();
            })
            .catch((e) => setError(apiError(e)))
            .finally(() => setBusy(false));
        }}
      >
        <label className="grid gap-1 text-sm">
          {t("domain.authority.operation")}
          <select
            className="h-10 rounded-md border bg-background px-3"
            value={action}
            disabled={busy}
            onChange={(event) => {
              setAction(event.target.value);
              setConfirm(false);
            }}
          >
            {[
              "prepare",
              "enable",
              "rotate",
              "retire",
              "rollback",
              "disable-check",
              "disable",
              "secondary-configure",
              "secondary-prepare",
              "backup-manifest",
            ].map((value) => (
              <option key={value} value={value}>
                {t(`domain.authority.action_${value}`)}
              </option>
            ))}
          </select>
        </label>
        {action === "secondary-configure" && (
          <div className="grid gap-2 sm:grid-cols-2">
            {(
              [
                "hostname",
                "address",
                "primary_address",
                "tsig_key_name",
              ] as const
            ).map((key) => (
              <label key={key} className="grid gap-1 text-sm">
                {t(`domain.authority.${key}`)}
                <Input
                  required
                  maxLength={key === "hostname" ? 253 : 128}
                  value={secondary[key]}
                  onChange={(event) =>
                    setSecondary((previous) => ({
                      ...previous,
                      [key]: event.target.value,
                    }))
                  }
                />
              </label>
            ))}
          </div>
        )}
        <p className="text-sm text-muted-foreground">
          {t("domain.authority.operation_help")}
        </p>
        <label className="flex items-start gap-2 text-sm">
          <input
            className="mt-1"
            type="checkbox"
            checked={confirm}
            onChange={(event) => setConfirm(event.target.checked)}
          />
          {t("domain.authority.confirm", { domain: domain.fqdn })}
        </label>
        <Button type="submit" disabled={busy || !confirm}>
          {t(busy ? "state.loading" : "domain.authority.execute")}
        </Button>
      </form>
    </section>
  );
}
