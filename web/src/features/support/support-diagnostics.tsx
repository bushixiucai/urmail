import { useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";

type Preview = {
  receipt: string;
  expires_at: string;
  bundle: { generated_at: string; fields: { name: string; value: string }[] };
};
export function SupportDiagnostics({
  disabled,
  onReceipt,
}: {
  disabled: boolean;
  onReceipt: (receipt: string) => void;
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  return (
    <fieldset
      className="rounded-md border p-3 space-y-3"
      disabled={disabled || busy}
    >
      <legend className="px-1 text-sm font-medium">
        {t("support.diagnostics_title")}
      </legend>
      <p className="text-sm text-muted-foreground">
        {t("support.diagnostics_scope")}
      </p>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          className="mt-1"
          checked={consent}
          onChange={(event) => {
            setConsent(event.target.checked);
            setPreview(null);
            setError("");
            onReceipt("");
          }}
        />
        {t("support.diagnostics_consent")}
      </label>
      <Button
        type="button"
        variant="outline"
        disabled={!consent || busy}
        onClick={() => {
          setBusy(true);
          setError("");
          onReceipt("");
          void request<Preview>("/api/v1/support/diagnostics", {
            method: "POST",
            body: { consent: true },
          })
            .then((result) => {
              setPreview(result);
            })
            .catch((e) => {
              setPreview(null);
              setError(errorMessage(e));
            })
            .finally(() => setBusy(false));
        }}
      >
        {t(busy ? "state.loading" : "support.diagnostics_preview")}
      </Button>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {preview && (
        <div className="space-y-2">
          <p className="text-xs text-muted-foreground">
            {t("support.diagnostics_expires", {
              time: new Date(preview.expires_at).toLocaleTimeString(),
            })}
          </p>
          <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 text-xs">
            {preview.bundle.fields.map((field) => (
              <div className="contents" key={field.name}>
                <dt className="break-all font-mono">{field.name}</dt>
                <dd className="break-all">{field.value}</dd>
              </div>
            ))}
          </dl>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              onChange={(event) =>
                onReceipt(event.target.checked ? preview.receipt : "")
              }
              key={preview.receipt}
            />
            {t("support.diagnostics_attach")}
          </label>
        </div>
      )}
    </fieldset>
  );
}
