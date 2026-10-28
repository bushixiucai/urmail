import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import {
  commitPeopleImport,
  peopleImportHistory,
  previewPeopleImport,
  type MetadataImportInput,
  type MetadataImportResult,
} from "./mailbox-people-api";

export function PeopleImportPanel() {
  const { t } = useTranslation();
  const apiError = useApiErrorMessage();
  const cache = useQueryClient();
  const [kind, setKind] = useState<"contacts" | "calendar">("contacts");
  const [mode, setMode] = useState<"skip" | "keep_both">("skip");
  const [file, setFile] = useState<File>();
  const [input, setInput] = useState<MetadataImportInput>();
  const [preview, setPreview] = useState<MetadataImportResult>();
  const [result, setResult] = useState<MetadataImportResult>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [key, setKey] = useState("");
  const request = useRef<AbortController | null>(null);
  const history = useQuery({
    queryKey: ["people-imports"],
    queryFn: peopleImportHistory,
  });
  const reset = () => {
    setPreview(undefined);
    setInput(undefined);
    setResult(undefined);
    setConfirmed(false);
    setNotice("");
    setError("");
  };
  const inspect = async () => {
    if (!file || busy) return;
    setBusy(true);
    reset();
    request.current = new AbortController();
    try {
      if (file.size > 1 << 20) throw new Error("IMPORT_SIZE");
      let content: string;
      try {
        content = new TextDecoder("utf-8", { fatal: true }).decode(
          await file.arrayBuffer(),
        );
      } catch {
        throw new Error("IMPORT_ENCODING");
      }
      const extension = file.name.split(".").pop()?.toLowerCase();
      const format =
        extension === "csv" ? "csv" : kind === "contacts" ? "vcf" : "ics";
      if (extension !== format) throw new Error("IMPORT_FORMAT");
      const source: MetadataImportInput = { kind, mode, format, content };
      const inspected = await previewPeopleImport(
        source,
        request.current.signal,
      );
      setInput({ ...source, digest: inspected.digest });
      setPreview(inspected);
      const storageKey = "urmail.metadata-import." + inspected.digest;
      const prior = sessionStorage.getItem(storageKey);
      const id = prior || crypto.randomUUID();
      sessionStorage.setItem(storageKey, id);
      setKey(id);
    } catch (e) {
      setError(
        e instanceof Error && e.message === "IMPORT_ENCODING"
          ? t("people.import_encoding")
          : e instanceof Error && e.message === "IMPORT_SIZE"
            ? t("people.import_size")
            : e instanceof Error && e.message === "IMPORT_FORMAT"
              ? t("people.import_format")
              : apiError(e),
      );
    } finally {
      setBusy(false);
      request.current = null;
    }
  };
  const commit = async () => {
    if (!input || !preview || !confirmed || busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    request.current = new AbortController();
    try {
      const imported = await commitPeopleImport(
        input,
        key,
        request.current.signal,
      );
      setResult(imported);
      setPreview(undefined);
      sessionStorage.removeItem("urmail.metadata-import." + input.digest);
      await Promise.all([
        cache.invalidateQueries({ queryKey: ["people-imports"] }),
        cache.invalidateQueries({ queryKey: ["people-contacts"] }),
        cache.invalidateQueries({ queryKey: ["people-events"] }),
      ]);
      setNotice(t("people.import_complete"));
    } catch (e) {
      setError(
        request.current?.signal.aborted
          ? t("people.import_cancel_pending")
          : apiError(e),
      );
      await cache.invalidateQueries({ queryKey: ["people-imports"] });
    } finally {
      setBusy(false);
      request.current = null;
    }
  };
  const summary = (receipt: MetadataImportResult) => (
    <p>
      {t("people.import_counts", {
        added: receipt.added,
        skipped: receipt.skipped,
        failed: receipt.errors.length,
      })}
    </p>
  );
  return (
    <section
      className="space-y-4 border-t pt-6"
      aria-label={t("people.import_title")}
    >
      <h2 className="text-lg font-semibold">{t("people.import_title")}</h2>
      <p className="max-w-prose text-sm text-muted-foreground">
        {t("people.import_help")}
      </p>
      <div className="flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm">
          {t("people.import_kind")}
          <select
            className="h-9 rounded-md border bg-background px-3"
            disabled={busy}
            aria-label={t("people.import_kind")}
            value={kind}
            onChange={(event) => {
              setKind(event.target.value as typeof kind);
              setFile(undefined);
              reset();
            }}
          >
            <option value="contacts">{t("page.webmail.contacts_title")}</option>
            <option value="calendar">{t("page.webmail.calendar_title")}</option>
          </select>
        </label>
        <label className="grid gap-1 text-sm">
          {t("people.duplicates")}
          <select
            className="h-9 rounded-md border bg-background px-3"
            disabled={busy}
            aria-label={t("people.duplicates")}
            value={mode}
            onChange={(event) => {
              setMode(event.target.value as typeof mode);
              reset();
            }}
          >
            <option value="skip">{t("people.skip_duplicates")}</option>
            <option value="keep_both">{t("people.keep_both")}</option>
          </select>
        </label>
        <label className="grid min-w-0 gap-1 text-sm">
          {t("people.import_file")}
          <Input
            key={kind}
            type="file"
            className="max-w-sm"
            disabled={busy}
            accept={kind === "contacts" ? ".vcf,.csv" : ".ics,.csv"}
            onChange={(event) => {
              setFile(event.target.files?.[0]);
              reset();
            }}
          />
        </label>
        <Button
          variant="outline"
          disabled={busy || !file}
          onClick={() => void inspect()}
        >
          {t("people.preview_import")}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {busy && (
        <p role="status" className="text-sm">
          {t("people.import_working")}
        </p>
      )}
      {preview && (
        <div className="space-y-3 rounded-md border p-4">
          <h3 className="font-semibold">
            {t("people.import_preview", {
              count: preview.valid,
              failed: preview.errors.length,
            })}
          </h3>
          <ul className="max-h-48 list-inside list-disc overflow-auto text-sm">
            {(preview.names ?? []).slice(0, 100).map((name, index) => (
              <li key={index} className="break-words">
                {name}
              </li>
            ))}
          </ul>
          {(preview.names?.length ?? 0) > 100 && (
            <p className="text-sm text-muted-foreground">
              {t("people.preview_first")}
            </p>
          )}
          {preview.errors.length > 0 && (
            <ul className="text-sm text-destructive">
              {preview.errors.map((item) => (
                <li key={item.index}>
                  {t("people.import_row_error", { index: item.index })}
                </li>
              ))}
            </ul>
          )}
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(event) => setConfirmed(event.target.checked)}
              disabled={busy}
            />
            {t(
              mode === "skip"
                ? "people.confirm_import_skip"
                : "people.confirm_import_keep",
            )}
          </label>
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={!confirmed || busy || preview.valid === 0}
              onClick={() => void commit()}
            >
              {t("people.commit_import")}
            </Button>
            <Button variant="outline" disabled={busy} onClick={reset}>
              {t("action.cancel")}
            </Button>
          </div>
        </div>
      )}
      {busy && (
        <Button variant="outline" onClick={() => request.current?.abort()}>
          {t("people.request_cancel")}
        </Button>
      )}
      {result && (
        <div role="status" className="text-sm">
          {summary(result)}
        </div>
      )}
      <details className="text-sm">
        <summary className="cursor-pointer">
          {t("people.import_history")}
        </summary>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void history.refetch()}
          disabled={history.isFetching}
        >
          {t("people.refresh")}
        </Button>
        {history.error && <p role="alert">{apiError(history.error)}</p>}
        <ul className="divide-y">
          {(history.data?.imports ?? []).map((entry) => (
            <li key={entry.id} className="py-2">
              <p className="font-medium">
                {t(
                  entry.kind === "import_contacts"
                    ? "page.webmail.contacts_title"
                    : "page.webmail.calendar_title",
                )}
              </p>
              {summary(entry.result)}
            </li>
          ))}
        </ul>
        {history.data?.imports.length === 0 && (
          <p>{t("people.import_history_empty")}</p>
        )}
      </details>
    </section>
  );
}
