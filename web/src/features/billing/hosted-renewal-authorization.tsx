import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiRequestError, request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";

export type HostedAuthorization = {
  id: number;
  merchant_id: number;
  state: string;
  version: number;
  url?: string;
  currency: string;
  quote_digest: string;
  expires_at: string;
  binding_id: number | null;
  last_error: string;
};
type Props = {
  merchants: { merchant_id: number; name: string }[];
  quoteDigest: string;
  canStart: boolean;
  onVerified: () => Promise<void>;
};
const base = "/api/v1/billing/auto-renewal/authorizations";
export function officialStripeHostedURL(
  value: string | undefined,
): string | null {
  try {
    const u = new URL(value ?? "");
    return u.protocol === "https:" &&
      u.hostname === "checkout.stripe.com" &&
      !u.username &&
      !u.password &&
      !u.port &&
      u.pathname.startsWith("/c/")
      ? u.href
      : null;
  } catch {
    return null;
  }
}

export function HostedRenewalAuthorization({
  merchants,
  quoteDigest,
  canStart,
  onVerified,
}: Props) {
  const { t } = useTranslation();
  const [items, setItems] = useState<HostedAuthorization[]>([]);
  const [merchant, setMerchant] = useState("");
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [unconfirmed, setUnconfirmed] = useState(false);
  const pendingKey = useRef<string | null>(null);
  const creationBlocked =
    unconfirmed ||
    items.some((i) => ["creating", "uncertain"].includes(i.state));
  useEffect(() => {
    setConsent(false);
  }, [quoteDigest]);
  const load = useCallback(async (signal?: AbortSignal) => {
    const suffix = pendingKey.current
      ? "?request_key=" + encodeURIComponent(pendingKey.current)
      : "";
    const result = await request<{
      authorizations?: HostedAuthorization[];
      authorization?: HostedAuthorization;
    }>(base + suffix, { signal });
    if (signal?.aborted) return;
    const rows =
      result.authorizations ??
      (result.authorization ? [result.authorization] : []);
    setItems(rows);
    if (rows.length && pendingKey.current) {
      pendingKey.current = null;
      setUnconfirmed(false);
    }
  }, []);
  // Without an approved hosted channel there is nothing to list; skip the
  // request so deployments without billing (and development) never surface a
  // failing call in the browser console.
  const hostedChannelReady = merchants.length > 0;
  useEffect(() => {
    if (!hostedChannelReady) return;
    const ac = new AbortController();
    void load(ac.signal).catch(() => {
      if (!ac.signal.aborted) setError(true);
    });
    return () => ac.abort();
  }, [load, hostedChannelReady]);
  const act = async (
    item: HostedAuthorization | null,
    action: "complete" | "reconcile" | "cancel" | "start",
  ) => {
    if (
      busy ||
      (action === "start" &&
        (!canStart ||
          !consent ||
          creationBlocked ||
          !merchants.some((m) => String(m.merchant_id) === merchant)))
    )
      return;
    setBusy(true);
    setError(false);
    try {
      if (action === "start") pendingKey.current ??= crypto.randomUUID();
      const result = await request<{ authorization: HostedAuthorization }>(
        item ? `${base}/${item.id}/${action}` : base,
        {
          method: "POST",
          idempotencyKey: item ? undefined : pendingKey.current!,
          body: item
            ? { version: item.version }
            : {
                merchant_id: Number(merchant),
                quote_digest: quoteDigest,
                confirmed: true,
              },
        },
      );
      pendingKey.current = null;
      setUnconfirmed(false);
      setConsent(false);
      setItems((current) => [
        result.authorization,
        ...current.filter((i) => i.id !== result.authorization.id),
      ]);
      if (result.authorization.state === "verified") await onVerified();
    } catch (cause) {
      setError(true);
      if (action === "start") {
        if (
          cause instanceof ApiRequestError &&
          cause.status >= 400 &&
          cause.status < 500 &&
          cause.status !== 408
        )
          pendingKey.current = null;
        else setUnconfirmed(true);
      }
      // GET recovers a lost response. Never replay an uncertain provider create.
      await load().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  const refresh = async () => {
    setBusy(true);
    setError(false);
    try {
      await load();
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      aria-label={t("billing_hosted.title")}
      className="space-y-3 rounded-md border p-4"
    >
      <h4 className="font-medium">{t("billing_hosted.title")}</h4>
      <p className="max-w-prose text-sm text-muted-foreground">
        {t("billing_hosted.explanation")}
      </p>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {t(
            unconfirmed
              ? "billing_hosted.uncertain_create"
              : "billing_hosted.failed",
          )}
        </p>
      )}
      <Button
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => void refresh()}
      >
        {t("billing_hosted.refresh")}
      </Button>
      <label className="grid max-w-lg gap-1 text-sm">
        {t("billing_hosted.channel")}
        <select
          className="h-10 min-w-0 rounded-md border bg-background px-3"
          value={merchant}
          disabled={busy || !canStart || creationBlocked}
          onChange={(e) => {
            setMerchant(e.target.value);
            setConsent(false);
          }}
        >
          <option value="">{t("billing_hosted.choose")}</option>
          {merchants.map((m) => (
            <option key={m.merchant_id} value={m.merchant_id}>
              {m.name}
            </option>
          ))}
        </select>
      </label>
      {!merchants.length && (
        <p className="text-sm">{t("billing_hosted.unavailable")}</p>
      )}
      <label className="flex max-w-prose items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={consent}
          disabled={busy || !canStart || !merchant || creationBlocked}
          onChange={(e) => setConsent(e.target.checked)}
        />
        {t("billing_hosted.consent")}
      </label>
      <Button
        type="button"
        disabled={busy || !canStart || !merchant || !consent || creationBlocked}
        onClick={() => void act(null, "start")}
      >
        {t("billing_hosted.start")}
      </Button>
      <ul className="divide-y text-sm">
        {items.map((item) => {
          const url =
            item.state === "ready" && Date.parse(item.expires_at) > Date.now()
              ? officialStripeHostedURL(item.url)
              : null;
          return (
            <li key={item.id} className="space-y-2 py-3">
              <p>
                {t("billing_hosted.receipt", {
                  id: item.id,
                  state: t(`billing_hosted.state.${item.state}`, {
                    defaultValue: t("billing_hosted.state.uncertain"),
                  }),
                })}
              </p>
              {item.state === "verified" && (
                <p>{t("billing_hosted.verified")}</p>
              )}
              {item.state === "ready" && !url && (
                <p role="alert">{t("billing_hosted.invalid_url")}</p>
              )}
              <div className="flex flex-wrap gap-2">
                {url && (
                  <a
                    href={url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex min-h-10 items-center rounded-md border px-3 underline"
                  >
                    {t("billing_hosted.open")}
                  </a>
                )}
                {item.state === "ready" && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void act(item, "complete")}
                  >
                    {t("billing_hosted.complete")}
                  </Button>
                )}
                {["creating", "uncertain"].includes(item.state) && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void act(item, "reconcile")}
                  >
                    {t("billing_hosted.reconcile")}
                  </Button>
                )}
                {["creating", "ready", "uncertain"].includes(item.state) && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void act(item, "cancel")}
                  >
                    {t("billing_hosted.cancel")}
                  </Button>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
