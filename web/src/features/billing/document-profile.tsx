import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const fields = [
  "name",
  "tax_id",
  "address",
  "phone",
  "email",
  "bank_name",
  "bank_account",
] as const;
type Party = Partial<Record<(typeof fields)[number], string>>;
interface Tax {
  configured: boolean;
  rate_basis_points: number;
  included_in_total: boolean;
}
interface Profile {
  party: Party;
  tax: Tax;
  locale: string;
}

export function DocumentProfile({ platform = false }: { platform?: boolean }) {
  const { t } = useTranslation();
  const endpoint = platform
    ? "/api/v1/platform/billing-document-profile"
    : "/api/v1/billing/document-profile";
  const [profile, setProfile] = useState<Profile>({
    party: {},
    tax: { configured: false, rate_basis_points: 0, included_in_total: true },
    locale: "zh-CN",
  });
  const [patch, setPatch] = useState<Party>({});
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const load = useCallback(
    async (signal?: AbortSignal) => {
      const result = await request<{ profile: Profile }>(endpoint, { signal });
      setProfile(result.profile);
      setPatch({});
      setLoaded(true);
    },
    [endpoint],
  );
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal).catch(() => {
      if (!controller.signal.aborted)
        setMessage(t("document_profile.load_failed"));
    });
    return () => controller.abort();
  }, [load, t]);
  const save = async () => {
    setBusy(true);
    setMessage("");
    try {
      await request(endpoint, {
        method: "PUT",
        body: {
          party: patch,
          locale: profile.locale || "zh-CN",
          ...(platform
            ? { tax: { ...profile.tax, included_in_total: true } }
            : {}),
        },
      });
      await load();
      setMessage(t("document_profile.saved"));
    } catch {
      setMessage(t("document_profile.save_failed"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      className="space-y-4 rounded-lg border p-4"
      aria-label={t("document_profile.title")}
    >
      <h2 className="text-lg font-semibold">
        {t(platform ? "document_profile.seller" : "document_profile.buyer")}
      </h2>
      <p className="text-sm text-muted-foreground">
        {t("document_profile.future_only")}
      </p>
      <p className="text-sm text-muted-foreground">
        {t("document_profile.private_hint")}
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        {fields.map((key) => (
          <label key={key} className="space-y-1 text-sm">
            {t(
              `invoice_pdf.${key === "name" ? (platform ? "seller" : "buyer") : key}`,
            )}
            <Input
              value={patch[key] ?? profile.party[key] ?? ""}
              maxLength={key === "address" ? 600 : 180}
              disabled={!loaded || busy}
              onChange={(event) =>
                setPatch((old) => ({ ...old, [key]: event.target.value }))
              }
            />
          </label>
        ))}
      </div>
      {platform && (
        <fieldset className="space-y-3">
          <legend className="text-sm font-medium">
            {t("document_profile.tax_policy")}
          </legend>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={profile.tax.configured}
              disabled={!loaded || busy}
              onChange={(e) =>
                setProfile((p) => ({
                  ...p,
                  tax: { ...p.tax, configured: e.target.checked },
                }))
              }
            />
            {t("document_profile.tax_enabled")}
          </label>
          <label className="block space-y-1 text-sm">
            {t("document_profile.rate_bps")}
            <Input
              type="number"
              min={0}
              max={10000}
              step={1}
              value={profile.tax.rate_basis_points}
              disabled={!loaded || busy || !profile.tax.configured}
              onChange={(e) =>
                setProfile((p) => ({
                  ...p,
                  tax: { ...p.tax, rate_basis_points: Number(e.target.value) },
                }))
              }
            />
          </label>
        </fieldset>
      )}
      {message && (
        <p role="status" className="break-words text-sm">
          {message}
        </p>
      )}
      <Button
        disabled={
          !loaded || busy || !(patch.name ?? profile.party.name ?? "").trim()
        }
        onClick={() => void save()}
      >
        {t("document_profile.save")}
      </Button>
    </section>
  );
}
