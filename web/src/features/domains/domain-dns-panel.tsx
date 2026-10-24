import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/ui/notice-dialog";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

type DNSRecord = {
  type: string;
  name: string;
  value: string;
  priority?: number;
  ttl?: number;
};
type Change = {
  kind: string;
  before?: DNSRecord & { id: string };
  after: DNSRecord;
};
type Plan = {
  id: string;
  kind: string;
  status: string;
  source_digest: string;
  before_records: DNSRecord[];
  desired_records: DNSRecord[];
  changes: { actions: Change[]; conflicts: string[] };
  expires_at: string;
  last_error_code?: string | null;
};
type Credential = {
  id: number;
  provider: string;
  zone_name: string;
  version: number;
  enabled: boolean;
  verified_at: string;
};
type Task = {
  id: number;
  domain_id: number;
  status: string;
  step: string;
  attempts: number;
  max_attempts: number;
  error_code?: string;
  next_attempt_at?: string;
};
type Domain = {
  id: number;
  fqdn: string;
  mode: string;
  status?: string;
  provisioning_state?: string;
};
type Intent = {
  plan?: Plan;
  action:
    | "confirm"
    | "cancel"
    | "rollback"
    | "reconcile"
    | "abandon"
    | "revoke"
    | "retry";
};
const selectClass =
  "h-10 w-full rounded-md border border-input bg-background px-3 text-sm";
const textareaClass =
  "min-h-24 w-full rounded-md border border-input bg-background p-3 font-mono text-xs";

export function DomainDNSPanel({ domain }: { domain: Domain }) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const lifetime = useRef<{ active: boolean; controller: AbortController }>({
    active: true,
    controller: new AbortController(),
  });
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [provider, setProvider] = useState("cloudflare");
  const [zone, setZone] = useState(domain.fqdn);
  const [token, setToken] = useState("");
  const [keyID, setKeyID] = useState("");
  const [keySecret, setKeySecret] = useState("");
  const [kind, setKind] = useState(
    domain.mode === "managed" ? "managed" : "manual",
  );
  const [imported, setImported] = useState("[]");
  const [reviewedWebsite, setReviewedWebsite] = useState(false);
  const [approved, setApproved] = useState<string | null>(null);
  const [reason, setReason] = useState("");
  const [intent, setIntent] = useState<Intent | null>(null);
  const [intentReady, setIntentReady] = useState(false);
  const beginIntent = (value: Intent) => {
    setIntentReady(false);
    setIntent(value);
  };
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const domainDisabled = domain.status === "disabled";
  const base = `/api/v1/domains/${domain.id}/dns`;
  const refresh = useCallback(async () => {
    const scope = lifetime.current;
    try {
      const [c, p, jobs] = await Promise.all([
        request<{ credentials: Credential[] }>(`${base}/credentials`, {
          signal: scope.controller.signal,
        }),
        request<{ plans: Plan[] }>(`${base}/plans`, {
          signal: scope.controller.signal,
        }),
        request<{ tasks: Task[] }>("/api/v1/onboarding/tasks", {
          signal: scope.controller.signal,
        }),
      ]);
      if (!scope.active) return;
      setCredentials(c.credentials ?? []);
      setPlans(p.plans ?? []);
      setTasks((jobs.tasks ?? []).filter((job) => job.domain_id === domain.id));
    } catch (err) {
      if (scope.active) setError(errorMessage(err));
    }
  }, [base, domain.id, errorMessage]);
  useEffect(() => {
    const scope = { active: true, controller: new AbortController() };
    lifetime.current = scope;
    void refresh();
    const timer = window.setInterval(() => void refresh(), 10000);
    return () => {
      scope.active = false;
      scope.controller.abort();
      window.clearInterval(timer);
    };
  }, [refresh]);
  const mutate = async (
    url: string,
    body: unknown,
    method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE" = "POST",
  ) => {
    const scope = lifetime.current;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await request(url, { method, body, signal: scope.controller.signal });
      if (!scope.active) return;
      setNotice(t("page.dns.saved"));
      setIntent(null);
      setApproved(null);
      await refresh();
    } catch (err) {
      if (scope.active) {
        setIntent(null);
        setApproved(null);
        setError(errorMessage(err));
        await refresh();
      }
    } finally {
      if (scope.active) setBusy(false);
    }
  };
  const saveCredential = async () => {
    const secret =
      provider === "cloudflare" || provider === "dnspod"
        ? { token }
        : { access_key_id: keyID, access_key_secret: keySecret };
    setToken("");
    setKeyID("");
    setKeySecret("");
    await mutate(
      `${base}/credentials`,
      { provider, zone, credentials: secret },
      "PUT",
    );
    if (lifetime.current.active) {
      setToken("");
      setKeyID("");
      setKeySecret("");
    }
  };
  const preview = async () => {
    let records: unknown;
    try {
      records = JSON.parse(imported);
      if (!Array.isArray(records)) throw new Error();
    } catch {
      setError(t("page.dns.import_invalid"));
      return;
    }
    await mutate(`${base}/plans`, {
      kind,
      imported_records: records,
      reviewed_website_records: reviewedWebsite,
    });
  };
  const executeIntent = async () => {
    if (!intent) return;
    if (intent.action === "revoke")
      return mutate(`${base}/credentials`, undefined, "DELETE");
    if (intent.action === "retry")
      return mutate(`/api/v1/domains/${domain.id}/onboarding/retry`, {
        reason,
      });
    if (!intent.plan) return;
    return mutate(`${base}/plans/${intent.plan.id}/${intent.action}`, {
      source_digest: intent.plan.source_digest,
      confirm: true,
      confirm_observed: true,
      confirm_unresolved: true,
      reason,
    });
  };
  const needsReason =
    intent !== null && !["confirm", "revoke"].includes(intent.action);
  return (
    <Card data-testid="domain-dns-panel">
      <CardHeader>
        <CardTitle>{t("page.dns.title", { domain: domain.fqdn })}</CardTitle>
        <CardDescription>{t("page.dns.identity_notice")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6 min-w-0">
        {error && (
          <p role="alert" className="break-words text-sm text-destructive">
            {error}
          </p>
        )}
        {notice && (
          <p role="status" className="text-sm text-muted-foreground">
            {notice}
          </p>
        )}
        <section className="space-y-3" aria-label={t("page.dns.progress")}>
          <h3 className="font-medium">{t("page.dns.progress")}</h3>
          <p className="text-sm text-muted-foreground">
            {t("page.dns.endpoint_notice")}
          </p>
          {tasks.length === 0 ? (
            <p className="text-sm">{t("page.dns.no_tasks")}</p>
          ) : (
            tasks.map((job) => (
              <div
                key={job.id}
                className="rounded-md border p-3 space-y-1 text-sm"
              >
                <p>
                  {t(`page.dns.step.${job.step}`)} ·{" "}
                  {t(`page.dns.state.${job.status}`)}
                </p>
                <p className="text-muted-foreground">
                  {t("page.dns.attempts", {
                    used: job.attempts,
                    total: job.max_attempts,
                  })}
                </p>
                {job.error_code && (
                  <p className="break-all">
                    {t("page.dns.dependency_failed")}{" "}
                    <code>{job.error_code}</code>
                  </p>
                )}
                {job.next_attempt_at && job.status === "pending" && (
                  <time>
                    {t("page.dns.next_attempt", {
                      time: new Date(job.next_attempt_at).toLocaleString(),
                    })}
                  </time>
                )}
              </div>
            ))
          )}
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => {
              setReason("");
              beginIntent({ action: "retry" });
            }}
          >
            {t("page.dns.retry")}
          </Button>
        </section>
        <section className="space-y-3" aria-label={t("page.dns.credentials")}>
          <h3 className="font-medium">{t("page.dns.credentials")}</h3>
          <p className="text-sm text-muted-foreground">
            {t("page.dns.credential_notice")}
          </p>
          {credentials.map((credential) => (
            <p key={credential.id} className="break-words text-sm">
              {t(`page.dns.provider.${credential.provider}`)} ·{" "}
              {credential.zone_name} ·{" "}
              {t("page.dns.version", { version: credential.version })} ·{" "}
              {t(
                credential.enabled
                  ? "page.dns.credential_enabled"
                  : "page.dns.credential_revoked",
              )}
            </p>
          ))}
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="text-sm space-y-1">
              <span>{t("page.dns.provider")}</span>
              <select
                className={selectClass}
                value={provider}
                onChange={(e) => {
                  setProvider(e.target.value);
                  setToken("");
                  setKeyID("");
                  setKeySecret("");
                }}
              >
                {["cloudflare", "aliyun", "dnspod", "tencent"].map((value) => (
                  <option key={value} value={value}>
                    {t(`page.dns.provider.${value}`)}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-sm space-y-1">
              <span>{t("page.dns.zone")}</span>
              <Input
                value={zone}
                onChange={(e) => setZone(e.target.value)}
                autoComplete="off"
              />
            </label>
          </div>
          {provider === "cloudflare" || provider === "dnspod" ? (
            <label className="block text-sm space-y-1">
              <span>
                {t(
                  provider === "dnspod"
                    ? "page.dns.legacy_token"
                    : "page.dns.token",
                )}
              </span>
              <Input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                autoComplete="new-password"
              />
            </label>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="text-sm space-y-1">
                <span>{t("page.dns.key_id")}</span>
                <Input
                  value={keyID}
                  onChange={(e) => setKeyID(e.target.value)}
                  autoComplete="off"
                />
              </label>
              <label className="text-sm space-y-1">
                <span>{t("page.dns.key_secret")}</span>
                <Input
                  type="password"
                  value={keySecret}
                  onChange={(e) => setKeySecret(e.target.value)}
                  autoComplete="new-password"
                />
              </label>
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={
                busy ||
                !zone ||
                (provider === "cloudflare" || provider === "dnspod"
                  ? !token
                  : !keyID || !keySecret)
              }
              onClick={() => void saveCredential()}
            >
              {t("page.dns.probe_save")}
            </Button>
            <Button
              variant="outline"
              disabled={busy || credentials.every((c) => !c.enabled)}
              onClick={() => beginIntent({ action: "revoke" })}
            >
              {t("page.dns.revoke")}
            </Button>
          </div>
        </section>
        <section className="space-y-3" aria-label={t("page.dns.preview")}>
          <h3 className="font-medium">{t("page.dns.preview")}</h3>
          <p className="text-sm text-muted-foreground">
            {t(
              domain.mode === "managed"
                ? "page.dns.managed_notice"
                : "page.dns.delegated_notice",
            )}
          </p>
          <label className="block text-sm space-y-1">
            <span>{t("page.dns.kind")}</span>
            <select
              className={selectClass}
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            >
              <option value={domain.mode === "managed" ? "managed" : "manual"}>
                {t(
                  domain.mode === "managed"
                    ? "page.dns.kind.managed"
                    : "page.dns.kind.manual",
                )}
              </option>
              <option value="provider">{t("page.dns.kind.provider")}</option>
            </select>
          </label>
          {kind !== "provider" && (
            <label className="block text-sm space-y-1">
              <span>{t("page.dns.import_label")}</span>
              <textarea
                className={textareaClass}
                value={imported}
                onChange={(e) => setImported(e.target.value)}
                spellCheck={false}
              />
            </label>
          )}
          {kind === "managed" && (
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={reviewedWebsite}
                onChange={(e) => setReviewedWebsite(e.target.checked)}
                className="mt-1"
              />
              <span>{t("page.dns.website_confirm")}</span>
            </label>
          )}
          <Button
            variant="outline"
            disabled={
              busy || domainDisabled || (kind === "managed" && !reviewedWebsite)
            }
            onClick={() => void preview()}
          >
            {t("page.dns.preview_only")}
          </Button>
        </section>
        <section className="space-y-3" aria-label={t("page.dns.plans")}>
          <h3 className="font-medium">{t("page.dns.plans")}</h3>
          {plans.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t("page.dns.no_plans")}
            </p>
          )}
          {plans.map((plan) => (
            <details
              key={plan.id}
              className="rounded-md border p-3 min-w-0"
              open={plan.status === "draft" || plan.status === "uncertain"}
            >
              <summary className="cursor-pointer break-words text-sm">
                {t(`page.dns.state.${plan.status}`)} ·{" "}
                {t(`page.dns.kind.${plan.kind}`)}
              </summary>
              <div className="mt-3 space-y-3 min-w-0">
                <p className="text-xs text-muted-foreground">
                  {t("page.dns.expires", {
                    time: new Date(plan.expires_at).toLocaleString(),
                  })}
                </p>
                {plan.last_error_code && (
                  <p className="text-sm break-all">
                    <code>{plan.last_error_code}</code>
                  </p>
                )}
                {plan.changes.conflicts.map((conflict) => (
                  <p
                    role="alert"
                    key={conflict}
                    className="text-sm text-destructive break-words"
                  >
                    {t("page.dns.conflict")} <code>{conflict}</code>
                  </p>
                ))}
                <div className="overflow-x-auto rounded border">
                  <table className="w-full min-w-[36rem] text-left text-xs">
                    <thead>
                      <tr className="border-b">
                        <th className="p-2">{t("page.dns.change")}</th>
                        <th className="p-2">{t("page.dns.record")}</th>
                        <th className="p-2">{t("page.dns.before")}</th>
                        <th className="p-2">{t("page.dns.after")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {plan.changes.actions.map((action, index) => (
                        <tr
                          className="border-b last:border-b-0"
                          key={`${action.after.type}:${action.after.name}:${index}`}
                        >
                          <td className="p-2">
                            {t(`page.dns.change.${action.kind}`)}
                          </td>
                          <td className="p-2 break-all">
                            {action.after.type} {action.after.name}
                          </td>
                          <td className="p-2 max-w-64 break-all">
                            {action.before?.value ?? t("page.dns.absent")}
                          </td>
                          <td className="p-2 max-w-64 break-all">
                            {action.after.value}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                <details>
                  <summary className="cursor-pointer text-sm">
                    {t("page.dns.all_records")}
                  </summary>
                  <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-3 text-xs">
                    {JSON.stringify(
                      {
                        before: plan.before_records,
                        desired: plan.desired_records,
                      },
                      null,
                      2,
                    )}
                  </pre>
                </details>
                {plan.status === "draft" && (
                  <label className="flex items-start gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={approved === plan.id}
                      onChange={(e) =>
                        setApproved(e.target.checked ? plan.id : null)
                      }
                      className="mt-1"
                    />
                    <span>{t("page.dns.plan_confirm")}</span>
                  </label>
                )}
                <div className="flex flex-wrap gap-2">
                  {plan.status === "draft" && (
                    <Button
                      disabled={
                        busy ||
                        approved !== plan.id ||
                        plan.changes.conflicts.length > 0 ||
                        Date.parse(plan.expires_at) <= Date.now()
                      }
                      onClick={() => beginIntent({ plan, action: "confirm" })}
                    >
                      {t("page.dns.confirm")}
                    </Button>
                  )}
                  {["draft", "confirmed"].includes(plan.status) && (
                    <Button
                      variant="outline"
                      disabled={busy}
                      onClick={() => {
                        setReason("");
                        beginIntent({ plan, action: "cancel" });
                      }}
                    >
                      {t("action.cancel")}
                    </Button>
                  )}
                  {plan.status === "completed" && plan.kind !== "manual" && (
                    <Button
                      variant="outline"
                      disabled={busy}
                      onClick={() => {
                        setReason("");
                        beginIntent({ plan, action: "rollback" });
                      }}
                    >
                      {t("page.dns.rollback")}
                    </Button>
                  )}
                  {plan.status === "uncertain" && (
                    <>
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() => {
                          setReason("");
                          beginIntent({ plan, action: "reconcile" });
                        }}
                      >
                        {t("page.dns.reconcile")}
                      </Button>
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() => {
                          setReason("");
                          beginIntent({ plan, action: "abandon" });
                        }}
                      >
                        {t("page.dns.abandon")}
                      </Button>
                    </>
                  )}
                </div>
              </div>
            </details>
          ))}
        </section>
        {needsReason && !intentReady && (
          <label className="block text-sm space-y-1">
            <span>{t("page.dns.reason")}</span>
            <Input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              maxLength={2000}
            />
          </label>
        )}
        <ConfirmDialog
          open={intent !== null && (!needsReason || intentReady)}
          onOpenChange={(open) => !open && setIntent(null)}
          title={t(`page.dns.intent.${intent?.action ?? "confirm"}`)}
          description={t(`page.dns.intent_desc.${intent?.action ?? "confirm"}`)}
          confirmLabel={t("action.confirm")}
          destructive={
            intent?.action === "rollback" ||
            intent?.action === "revoke" ||
            intent?.action === "abandon"
          }
          onConfirm={executeIntent}
        />
        {intent && needsReason && !intentReady && (
          <div className="flex flex-wrap items-center gap-2">
            <p role="status" className="text-sm text-muted-foreground">
              {t("page.dns.reason_required")}
            </p>
            <Button
              disabled={reason.trim().length < 10}
              onClick={() => setIntentReady(true)}
            >
              {t("action.continue")}
            </Button>
            <Button variant="outline" onClick={() => setIntent(null)}>
              {t("action.cancel")}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
