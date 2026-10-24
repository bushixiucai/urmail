import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

interface Rule {
  header: string;
  match: string;
  value: string;
  action: string;
  folder?: string;
}
interface Vacation {
  enabled: boolean;
  subject: string;
  message: string;
  days: number;
  from: string;
  until: string;
}
interface Rules {
  rules: Rule[] | null;
  vacation: Vacation;
}
interface Session {
  mailbox: string;
  rules: Rules;
  csrf: string;
  state: string;
  version: string;
  expires_at: string;
}
const initialVacation = (): Vacation => ({
  enabled: false,
  subject: "",
  message: "",
  days: 7,
  from: new Date().toISOString(),
  until: new Date(Date.now() + 7 * 86400000).toISOString(),
});

export function SievePage() {
  const { t } = useTranslation();
  const [session, setSession] = useState<Session | null>(null);
  const [rules, setRules] = useState<Rule[]>([]);
  const [vacation, setVacation] = useState<Vacation>(initialVacation);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    setConfirmed(false);
    fetch("/api/v1/mailbox/sieve/session", {
      credentials: "same-origin",
      signal: controller.signal,
    })
      .then(async (res) => {
        if (!res.ok) throw new Error();
        return res.json() as Promise<Session>;
      })
      .then((value) => {
        setSession(value);
        setRules(value.rules.rules ?? []);
        if (value.rules.vacation?.days > 0) setVacation(value.rules.vacation);
      })
      .catch(() => {
        if (!controller.signal.aborted)
          setError(t("page.sieve.reauthenticate"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [t, refresh]);
  const update = (index: number, change: Partial<Rule>) => {
    setRules((current) =>
      current.map((rule, i) => (i === index ? { ...rule, ...change } : rule)),
    );
    setConfirmed(false);
  };
  const save = async () => {
    if (!session || !confirmed) return;
    setBusy(true);
    setError("");
    try {
      const res = await fetch("/api/v1/mailbox/sieve/rules", {
        method: "PUT",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          "X-Urmail-Sieve-CSRF": session.csrf,
        },
        body: JSON.stringify({
          rules: { rules, vacation },
          confirmed: true,
          expected_version: session.version,
        }),
      });
      if (!res.ok)
        throw new Error(
          res.status === 502
            ? "activation"
            : res.status === 409
              ? "conflict"
              : "save",
        );
      setSession((await res.json()) as Session);
      setConfirmed(false);
    } catch (failure) {
      setError(
        t(
          failure instanceof Error && failure.message === "conflict"
            ? "page.sieve.version_conflict"
            : failure instanceof Error && failure.message === "activation"
              ? "page.sieve.activation_failed"
              : "page.sieve.save_failed",
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  const close = async () => {
    if (!session) return;
    setBusy(true);
    try {
      const response = await fetch("/api/v1/mailbox/sieve/logout", {
        method: "POST",
        credentials: "same-origin",
        headers: { "X-Urmail-Sieve-CSRF": session.csrf },
      });
      if (!response.ok) throw new Error();
      setSession(null);
      setError(t("page.sieve.reauthenticate"));
    } catch {
      setError(t("page.sieve.save_failed"));
    } finally {
      setBusy(false);
    }
  };
  const selectClass =
    "h-10 rounded-md border border-input bg-background px-3 text-sm w-full";
  return (
    <main className="mx-auto max-w-3xl space-y-6 px-4 py-8">
      <header className="space-y-2">
        <a href="/mail/" className="text-sm underline">
          {t("nav.webmail")}
        </a>
        <h1 className="text-2xl font-semibold">{t("page.sieve.title")}</h1>
        <p className="text-sm text-muted-foreground">
          {t("page.webmail.independent_identity")}
        </p>
        {session && <p className="break-all">{session.mailbox}</p>}
      </header>
      {session && (
        <details className="space-y-2 rounded border p-3">
          <summary>{t("page.sieve.diff")}</summary>
          <div className="grid gap-3 md:grid-cols-2">
            <section>
              <h2>{t("page.sieve.before")}</h2>
              <pre className="whitespace-pre-wrap break-all text-xs">
                {JSON.stringify(session.rules, null, 2)}
              </pre>
            </section>
            <section>
              <h2>{t("page.sieve.after")}</h2>
              <pre className="whitespace-pre-wrap break-all text-xs">
                {JSON.stringify({ rules, vacation }, null, 2)}
              </pre>
            </section>
          </div>
        </details>
      )}
      {error && (
        <div className="space-y-2">
          <p role="alert" className="text-destructive">
            {error}
          </p>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => setRefresh((value) => value + 1)}
          >
            {t("page.sieve.reload")}
          </Button>
        </div>
      )}
      {!session && busy && <p role="status">{t("state.loading")}</p>}
      {session?.state === "legacy_managesieve" && (
        <section className="space-y-4" aria-live="polite">
          <p>{t("page.sieve.legacy_managesieve")}</p>
          <a
            className="text-primary underline"
            href="/mail/?_task=settings&_action=plugin.managesieve"
          >
            {t("page.sieve.open_managesieve")}
          </a>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void close()}
          >
            {t("page.sieve.close")}
          </Button>
        </section>
      )}
      {session && session.state !== "legacy_managesieve" && (
        <>
          <p role="status" className="text-sm text-muted-foreground">
            {t(
              session.state === "active"
                ? "page.sieve.active"
                : "page.sieve.desired",
            )}
          </p>
          <section className="space-y-4">
            <h2 className="text-lg font-semibold">{t("page.sieve.filters")}</h2>
            {rules.map((rule, index) => (
              <fieldset
                key={index}
                className="grid gap-3 rounded-md border p-4 sm:grid-cols-2"
              >
                <legend className="px-1">
                  {t("page.sieve.rule_number", { number: index + 1 })}
                </legend>
                <label>
                  {t("page.sieve.header")}
                  <select
                    className={selectClass}
                    value={rule.header}
                    onChange={(event) =>
                      update(index, { header: event.target.value })
                    }
                  >
                    {["subject", "from", "to", "cc"].map((value) => (
                      <option key={value} value={value}>
                        {t(`page.sieve.header_${value}`)}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  {t("page.sieve.match")}
                  <select
                    className={selectClass}
                    value={rule.match}
                    onChange={(event) =>
                      update(index, { match: event.target.value })
                    }
                  >
                    {["contains", "is"].map((value) => (
                      <option key={value} value={value}>
                        {t(`page.sieve.match_${value}`)}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="sm:col-span-2">
                  {t("page.sieve.value")}
                  <Input
                    maxLength={2000}
                    value={rule.value}
                    onChange={(event) =>
                      update(index, { value: event.target.value })
                    }
                  />
                </label>
                <label>
                  {t("page.sieve.action")}
                  <select
                    className={selectClass}
                    value={rule.action}
                    onChange={(event) =>
                      update(index, { action: event.target.value })
                    }
                  >
                    {["fileinto", "keep", "discard"].map((value) => (
                      <option key={value} value={value}>
                        {t(`page.sieve.action_${value}`)}
                      </option>
                    ))}
                  </select>
                </label>
                {rule.action === "fileinto" && (
                  <label>
                    {t("page.sieve.folder")}
                    <Input
                      maxLength={128}
                      value={rule.folder ?? ""}
                      onChange={(event) =>
                        update(index, { folder: event.target.value })
                      }
                    />
                  </label>
                )}
                <Button
                  variant="outline"
                  onClick={() => {
                    setRules((current) =>
                      current.filter((_, i) => i !== index),
                    );
                    setConfirmed(false);
                  }}
                >
                  {t("action.delete")}
                </Button>
              </fieldset>
            ))}
            <Button
              variant="outline"
              disabled={rules.length >= 50}
              onClick={() => {
                setRules((current) => [
                  ...current,
                  {
                    header: "subject",
                    match: "contains",
                    value: "",
                    action: "fileinto",
                    folder: "",
                  },
                ]);
                setConfirmed(false);
              }}
            >
              {t("page.sieve.add_rule")}
            </Button>
          </section>
          <section className="space-y-4 rounded-md border p-4">
            <h2 className="text-lg font-semibold">
              {t("page.sieve.vacation")}
            </h2>
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={vacation.enabled}
                onChange={(event) => {
                  setVacation({ ...vacation, enabled: event.target.checked });
                  setConfirmed(false);
                }}
              />
              {t("page.sieve.vacation_enabled")}
            </label>
            {vacation.enabled && (
              <>
                <label>
                  {t("page.sieve.subject")}
                  <Input
                    maxLength={200}
                    value={vacation.subject}
                    onChange={(event) => {
                      setVacation({ ...vacation, subject: event.target.value });
                      setConfirmed(false);
                    }}
                  />
                </label>
                <label className="block">
                  {t("page.sieve.message")}
                  <textarea
                    className="mt-2 min-h-32 w-full rounded-md border bg-background p-3"
                    maxLength={8192}
                    value={vacation.message}
                    onChange={(event) => {
                      setVacation({ ...vacation, message: event.target.value });
                      setConfirmed(false);
                    }}
                  />
                </label>
                <div className="grid gap-4 sm:grid-cols-2">
                  {(["from", "until"] as const).map((field) => (
                    <label key={field}>
                      {t(`page.sieve.${field}_utc`)}
                      <Input
                        type="datetime-local"
                        value={new Date(vacation[field])
                          .toISOString()
                          .slice(0, 16)}
                        onChange={(event) => {
                          if (event.target.value)
                            setVacation({
                              ...vacation,
                              [field]: new Date(
                                `${event.target.value}Z`,
                              ).toISOString(),
                            });
                          setConfirmed(false);
                        }}
                      />
                    </label>
                  ))}
                </div>
                <label>
                  {t("page.sieve.days")}
                  <Input
                    type="number"
                    min={1}
                    max={30}
                    value={vacation.days}
                    onChange={(event) => {
                      setVacation({
                        ...vacation,
                        days: Number(event.target.value),
                      });
                      setConfirmed(false);
                    }}
                  />
                </label>
                <p className="text-sm text-muted-foreground">
                  {t("page.sieve.loop_prevention")}
                </p>
              </>
            )}
          </section>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            {t("page.sieve.confirm")}
          </label>
          <Button disabled={busy || !confirmed} onClick={() => void save()}>
            {t(busy ? "state.loading" : "page.sieve.save")}
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void close()}
          >
            {t("page.sieve.close")}
          </Button>
        </>
      )}
    </main>
  );
}
