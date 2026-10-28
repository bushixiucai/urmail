import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  getMailImport,
  listMailImports,
  MailImportJob,
  MailImportItem,
  MAX_MAIL_IMPORT_BYTES,
  mutateMailImport,
  previewMailImport,
} from "./mailbox-import-api";

interface Props {
  open: boolean;
  onOpenChange(open: boolean): void;
  folder: string;
  validity: number;
  onImported(): void;
}

function folderName(encoded: string): string {
  try {
    if (!encoded.startsWith("b64.")) return encoded;
    const raw = atob(encoded.slice(4).replace(/-/g, "+").replace(/_/g, "/"));
    return new TextDecoder("utf-8", { fatal: true }).decode(
      Uint8Array.from(raw, (value) => value.charCodeAt(0)),
    );
  } catch {
    return encoded;
  }
}
async function fileBase64(file: File, signal: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    const abort = () => reader.abort();
    const finish = () => signal.removeEventListener("abort", abort);
    reader.onload = () => {
      finish();
      resolve(String(reader.result).split(",", 2)[1] ?? "");
    };
    reader.onerror = () => {
      finish();
      reject(reader.error);
    };
    reader.onabort = () => {
      finish();
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) {
      finish();
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    reader.readAsDataURL(file);
  });
}

export function MailboxImportDialog({
  open,
  onOpenChange,
  folder,
  validity,
  onImported,
}: Props) {
  const { t, i18n } = useTranslation();
  const [file, setFile] = useState<File | null>(null);
  const [format, setFormat] = useState<"eml" | "mbox">("eml");
  const [duplicates, setDuplicates] = useState<"skip" | "keep_both">("skip");
  const [jobs, setJobs] = useState<MailImportJob[]>([]);
  const [job, setJob] = useState<MailImportJob | null>(null);
  const [selected, setSelected] = useState<number[]>([]);
  const [password, setPassword] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [retryConfirmed, setRetryConfirmed] = useState(false);
  const [reason, setReason] = useState("");
  const [reconciledVersion, setReconciledVersion] = useState<number | null>(
    null,
  );
  const [busy, setBusy] = useState(false);
  const [pauseRequested, setPauseRequested] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const controller = useRef<AbortController | null>(null);
  const pause = useRef(false);
  const previewKey = useRef<string | null>(null);
  const revision = useRef(0);
  const prefix = "page.webmail.import.";
  const count = (status: string) =>
    job?.counts?.[status as keyof NonNullable<MailImportJob["counts"]>] ??
    job?.items.filter((item) => item.status === status).length ??
    0;
  const mib = (bytes: number) =>
    t(prefix + "mib", {
      value: new Intl.NumberFormat(i18n.language, {
        maximumFractionDigits: 1,
      }).format(bytes / 1024 / 1024),
    });
  const report = (cause: unknown) =>
    setError(
      cause instanceof ApiRequestError
        ? t(`error.${cause.code}`, { defaultValue: t(prefix + "failed") })
        : t(prefix + "unconfirmed"),
    );
  const previewPhase = job && ["preview", "ready"].includes(job.status);
  const retrySelectable = (item: MailImportItem) =>
    reconciledVersion === job?.version &&
    job?.status !== "canceled" &&
    ["unknown", "failed"].includes(item.status) &&
    item.reconcile_absent === true &&
    !!item.reconciled_at &&
    Date.parse(item.reconciled_at) > Date.now() - 5 * 60 * 1000;
  const selectable = (item: MailImportItem) =>
    (item.status === "ready" && previewPhase) || retrySelectable(item);
  const retrySelectionValid =
    !!job &&
    selected.length > 0 &&
    selected.every((index) =>
      job.items.some((item) => item.index === index && retrySelectable(item)),
    );

  useEffect(() => {
    revision.current++;
    controller.current?.abort();
    pause.current = true;
    setPauseRequested(false);
    previewKey.current = null;
    setPassword("");
    setSelected([]);
    setConfirmed(false);
    setRetryConfirmed(false);
    setReason("");
    setReconciledVersion(null);
    setJob(null);
    setFile(null);
    setFormat("eml");
    setDuplicates("skip");
    setError(null);
    setBusy(false);
    if (!open) return;
    const ac = new AbortController();
    controller.current = ac;
    void listMailImports(ac.signal)
      .then((response) => {
        if (!ac.signal.aborted) setJobs(response.jobs ?? []);
      })
      .catch((cause) => {
        if (!ac.signal.aborted) report(cause);
      });
    return () => {
      pause.current = true;
      controller.current?.abort();
    };
    // Locale changes must not discard an in-progress import or a password edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, folder, validity]);

  const load = async (id: number) => {
    controller.current?.abort();
    const ac = new AbortController();
    controller.current = ac;
    const generation = ++revision.current;
    setPassword("");
    setSelected([]);
    setConfirmed(false);
    setRetryConfirmed(false);
    setReconciledVersion(null);
    setError(null);
    setBusy(true);
    try {
      const response = await getMailImport(id, ac.signal);
      if (generation === revision.current && !ac.signal.aborted)
        setJob(response.job);
    } catch (cause) {
      if (!ac.signal.aborted && generation === revision.current) report(cause);
    } finally {
      if (generation === revision.current) setBusy(false);
    }
  };
  const preview = async () => {
    if (
      !file ||
      busy ||
      !validity ||
      file.size > MAX_MAIL_IMPORT_BYTES ||
      file.size === 0
    )
      return;
    controller.current?.abort();
    const ac = new AbortController();
    controller.current = ac;
    const generation = ++revision.current;
    if (job && Date.parse(job.expires_at) <= Date.now())
      previewKey.current = null;
    previewKey.current ??= crypto.randomUUID();
    setBusy(true);
    setError(null);
    setJob(null);
    setSelected([]);
    setPassword("");
    setConfirmed(false);
    setReconciledVersion(null);
    try {
      const content = await fileBase64(file, ac.signal);
      const response = await previewMailImport(
        {
          format,
          filename: file.name,
          content_base64: content,
          folder_enc: folder,
          uid_validity: validity,
          duplicate_policy: duplicates,
        },
        previewKey.current,
        ac.signal,
      );
      if (generation === revision.current && !ac.signal.aborted) {
        setJob(response.job);
        setJobs((current) => [
          response.job,
          ...current.filter((item) => item.id !== response.job.id),
        ]);
      }
    } catch (cause) {
      if (!ac.signal.aborted && generation === revision.current) report(cause);
    } finally {
      if (generation === revision.current) setBusy(false);
    }
  };
  const act = async (
    action: "confirm" | "resume" | "reconcile" | "retry" | "cancel",
  ) => {
    if (!job || busy || (action !== "cancel" && !password)) return;
    if (action === "retry" && !retrySelectionValid) {
      setPassword("");
      setReconciledVersion(null);
      setError(t(prefix + "review_again"));
      return;
    }
    const current = job;
    const secret = password;
    setPassword("");
    setBusy(true);
    setError(null);
    pause.current = false;
    setPauseRequested(false);
    const generation = ++revision.current;
    controller.current?.abort();
    const ac = new AbortController();
    controller.current = ac;
    const body: Record<string, unknown> = { version: current.version };
    if (action !== "cancel") body.current_password = secret;
    if (action === "confirm")
      Object.assign(body, {
        preview_digest: current.preview_digest,
        selected_indices: selected,
        confirmed: true,
      });
    if (action === "retry")
      Object.assign(body, {
        indices: selected,
        confirmed_not_appended: true,
        reason: reason.trim(),
      });
    try {
      let response = await mutateMailImport(
        current.id,
        action,
        body,
        ac.signal,
      );
      for (let batch = 0; ; batch++) {
        if (generation !== revision.current || ac.signal.aborted) return;
        const next = response.job;
        setJob(next);
        setJobs((list) => [
          next,
          ...list.filter((item) => item.id !== next.id),
        ]);
        setSelected([]);
        setConfirmed(false);
        setRetryConfirmed(false);
        setReconciledVersion(action === "reconcile" ? next.version : null);
        if (next.items.some((item) => item.status === "imported")) onImported();
        const unknown =
          next.counts?.unknown ??
          next.items.filter((item) => item.status === "unknown").length;
        const pending =
          next.counts?.pending ??
          next.items.filter((item) => item.status === "pending").length;
        if (
          !["confirm", "resume"].includes(action) ||
          pause.current ||
          unknown > 0 ||
          pending === 0 ||
          !["confirmed", "pending"].includes(next.status) ||
          batch >= 19
        )
          break;
        response = await mutateMailImport(
          next.id,
          "resume",
          { version: next.version, current_password: secret },
          ac.signal,
        );
      }
    } catch (cause) {
      if (generation !== revision.current || ac.signal.aborted) return;
      report(cause);
      setReconciledVersion(null);
      // A lost response is not a reason to append again. Reload durable facts.
      try {
        const response = await getMailImport(current.id, ac.signal);
        if (generation === revision.current && !ac.signal.aborted)
          setJob(response.job);
      } catch {
        /* Preserve the original failure. */
      }
    } finally {
      if (generation === revision.current) {
        setBusy(false);
        setPassword("");
      }
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          revision.current++;
          pause.current = true;
          controller.current?.abort();
          setPassword("");
        }
        onOpenChange(next);
      }}
    >
      <DialogContent className="max-w-3xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t(prefix + "title")}</DialogTitle>
          <DialogDescription>{t(prefix + "scope")}</DialogDescription>
        </DialogHeader>
        <p className="text-sm break-all">
          {t(prefix + "target", {
            folder: folderName(job?.folder_enc ?? folder),
          })}
        </p>
        {(busy || (pauseRequested && count("pending") > 0)) && (
          <p
            role="status"
            aria-live="polite"
            className="text-sm text-muted-foreground"
          >
            {t(
              prefix +
                (pauseRequested ? (busy ? "pausing" : "paused") : "working"),
            )}
          </p>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="space-y-1 text-sm">
            {t(prefix + "file")}
            <Input
              type="file"
              accept=".eml,.mbox,message/rfc822,application/mbox"
              disabled={busy}
              onChange={(event) => {
                const next = event.target.files?.[0] ?? null;
                previewKey.current = null;
                setFile(next);
                setJob(null);
                setSelected([]);
                setConfirmed(false);
                setPassword("");
                setError(
                  next && (next.size > MAX_MAIL_IMPORT_BYTES || next.size === 0)
                    ? t(prefix + "limits")
                    : null,
                );
                if (next?.name.toLowerCase().endsWith(".mbox"))
                  setFormat("mbox");
                else setFormat("eml");
              }}
            />
          </label>
          <label className="space-y-1 text-sm">
            {t(prefix + "format")}
            <select
              className="h-10 w-full rounded-md border border-input bg-background px-3"
              value={format}
              disabled={busy}
              onChange={(event) => {
                previewKey.current = null;
                setFormat(event.target.value as "eml" | "mbox");
                setPassword("");
                setSelected([]);
                setConfirmed(false);
                setJob(null);
              }}
            >
              <option value="eml">EML</option>
              <option value="mbox">MBOX</option>
            </select>
          </label>
          <label className="space-y-1 text-sm">
            {t(prefix + "duplicates")}
            <select
              className="h-10 w-full rounded-md border border-input bg-background px-3"
              value={duplicates}
              disabled={busy}
              onChange={(event) => {
                previewKey.current = null;
                setDuplicates(event.target.value as "skip" | "keep_both");
                setPassword("");
                setSelected([]);
                setConfirmed(false);
                setJob(null);
              }}
            >
              <option value="skip">{t(prefix + "skip_duplicates")}</option>
              <option value="keep_both">{t(prefix + "keep_both")}</option>
            </select>
          </label>
          <div className="flex items-end">
            <Button
              type="button"
              variant="outline"
              disabled={
                busy ||
                !file ||
                file.size === 0 ||
                file.size > MAX_MAIL_IMPORT_BYTES ||
                !validity
              }
              onClick={() => void preview()}
            >
              {t(prefix + "preview")}
            </Button>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">{t(prefix + "limits")}</p>
        {job && (
          <section className="space-y-3" aria-label={t(prefix + "receipt")}>
            <div className="space-y-1">
              <h3 className="font-medium break-all">{job.filename}</h3>
              <p className="text-sm">
                {t(prefix + "summary", {
                  total: job.total,
                  size: mib(job.bytes),
                  status: t(prefix + "status_" + job.status, {
                    defaultValue: t(prefix + "status_unknown"),
                  }),
                })}
              </p>
              <p className="text-sm" role="status" aria-live="polite">
                {t(prefix + "progress", {
                  imported: count("imported"),
                  pending: count("pending") + count("importing"),
                  unknown: count("unknown"),
                  failed: count("failed"),
                })}
              </p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => void load(job.id)}
              >
                {t(prefix + "refresh_receipt")}
              </Button>
              {previewPhase && (
                <p className="text-xs text-muted-foreground">
                  {t(prefix + "expires", {
                    time: new Date(job.expires_at).toLocaleString(
                      i18n.language,
                    ),
                  })}
                </p>
              )}
            </div>
            {job.quota && (
              <p className="text-xs text-muted-foreground">
                {t(prefix + "quota", {
                  used: mib(job.quota.used_bytes),
                  limit:
                    job.quota.quota_bytes < 0
                      ? t(prefix + "unlimited")
                      : mib(job.quota.quota_bytes),
                })}
              </p>
            )}
            {count("unknown") > 0 && (
              <p className="text-sm text-destructive">
                {t(prefix + "unknown_help")}
              </p>
            )}
            {previewPhase && (
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    setSelected(
                      job.items
                        .filter((item) => item.status === "ready")
                        .map((item) => item.index),
                    )
                  }
                >
                  {t(prefix + "select_ready")}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => setSelected([])}
                >
                  {t(prefix + "clear_selection")}
                </Button>
              </div>
            )}
            <ul
              className="max-h-64 overflow-y-auto divide-y rounded-md border border-border"
              aria-label={t(prefix + "messages")}
            >
              {job.items.map((item) => (
                <li key={item.index} className="flex items-start gap-3 p-3">
                  <input
                    className="mt-1 h-4 w-4 shrink-0"
                    type="checkbox"
                    aria-label={t(prefix + "select_message", {
                      index: item.index,
                      subject: item.subject || t(prefix + "no_subject"),
                    })}
                    checked={selected.includes(item.index)}
                    disabled={busy || !selectable(item)}
                    onChange={(event) =>
                      setSelected((current) =>
                        event.target.checked
                          ? [...current, item.index]
                          : current.filter((index) => index !== item.index),
                      )
                    }
                  />
                  <div className="min-w-0 flex-1">
                    <p className="break-words text-sm">
                      {item.index}. {item.subject || t(prefix + "no_subject")}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {t(prefix + "status_" + item.status, {
                        defaultValue: t(prefix + "status_unknown"),
                      })}{" "}
                      · {mib(item.size)}
                    </p>
                    {Boolean(item.duplicate_of) && (
                      <p className="text-xs text-muted-foreground">
                        {t(prefix + "duplicate_of", {
                          index: item.duplicate_of,
                        })}
                      </p>
                    )}
                    {(item.duplicate_uids?.length ?? 0) > 0 && (
                      <p className="text-xs text-muted-foreground">
                        {t(prefix + "exists_in_target")}
                      </p>
                    )}
                    {item.error_code && item.status !== "duplicate" && (
                      <p className="text-xs text-destructive">
                        {t("error." + item.error_code, {
                          defaultValue: t(prefix + "row_failed"),
                        })}
                      </p>
                    )}
                  </div>
                </li>
              ))}
            </ul>
            {[
              "preview",
              "ready",
              "confirmed",
              "pending",
              "running",
              "uncertain",
              "held",
              "canceled",
              "unknown",
              "failed",
              "partial",
              "cancel_requested",
            ].includes(job.status) &&
              (job.status !== "canceled" || count("unknown") > 0) && (
                <div className="space-y-3">
                  <label className="block space-y-1 text-sm">
                    {t(prefix + "password")}
                    <Input
                      type="password"
                      autoComplete="current-password"
                      value={password}
                      disabled={busy}
                      onChange={(event) => setPassword(event.target.value)}
                    />
                  </label>
                  {previewPhase && (
                    <label className="flex items-start gap-2 text-sm">
                      <input
                        type="checkbox"
                        className="mt-1 h-4 w-4 shrink-0"
                        checked={confirmed}
                        disabled={busy}
                        onChange={(event) => setConfirmed(event.target.checked)}
                      />
                      {t(prefix + "confirm_scope", { count: selected.length })}
                    </label>
                  )}
                  <div className="flex flex-wrap gap-2">
                    {previewPhase && (
                      <Button
                        type="button"
                        disabled={
                          busy || !confirmed || !selected.length || !password
                        }
                        onClick={() => void act("confirm")}
                      >
                        {t(prefix + "confirm")}
                      </Button>
                    )}
                    {count("pending") > 0 &&
                      !["held", "canceled"].includes(job.status) &&
                      (job.status !== "canceled" || count("unknown") > 0) && (
                        <Button
                          type="button"
                          disabled={busy || !password || count("unknown") > 0}
                          onClick={() => void act("resume")}
                        >
                          {t(prefix + "resume")}
                        </Button>
                      )}
                    {count("unknown") + count("failed") > 0 && (
                      <Button
                        type="button"
                        variant="outline"
                        disabled={busy || !password}
                        onClick={() => void act("reconcile")}
                      >
                        {t(prefix + "reconcile")}
                      </Button>
                    )}
                    <Button
                      type="button"
                      variant="outline"
                      disabled={busy || job.status === "canceled"}
                      onClick={() => void act("cancel")}
                    >
                      {t(prefix + "cancel")}
                    </Button>
                    {busy && (
                      <Button
                        type="button"
                        variant="outline"
                        onClick={() => {
                          pause.current = true;
                          setPauseRequested(true);
                        }}
                      >
                        {t(prefix + "pause")}
                      </Button>
                    )}
                  </div>
                  {job.status !== "canceled" &&
                    reconciledVersion === job.version &&
                    count("unknown") + count("failed") > 0 && (
                      <div className="space-y-2">
                        <p className="text-sm text-destructive">
                          {t(prefix + "retry_help")}
                        </p>
                        <label className="block space-y-1 text-sm">
                          {t(prefix + "reason")}
                          <Input
                            value={reason}
                            maxLength={500}
                            disabled={busy}
                            onChange={(event) => setReason(event.target.value)}
                          />
                        </label>
                        <label className="flex items-start gap-2 text-sm">
                          <input
                            type="checkbox"
                            className="mt-1 h-4 w-4 shrink-0"
                            checked={retryConfirmed}
                            disabled={busy}
                            onChange={(event) =>
                              setRetryConfirmed(event.target.checked)
                            }
                          />
                          {t(prefix + "not_appended")}
                        </label>
                        <Button
                          type="button"
                          variant="outline"
                          disabled={
                            busy ||
                            !retryConfirmed ||
                            reason.trim().length < 10 ||
                            !retrySelectionValid ||
                            !password
                          }
                          onClick={() => void act("retry")}
                        >
                          {t(prefix + "retry")}
                        </Button>
                      </div>
                    )}
                </div>
              )}
          </section>
        )}
        <section className="space-y-2">
          <h3 className="text-sm font-medium">{t(prefix + "history")}</h3>
          {jobs.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t(prefix + "history_empty")}
            </p>
          ) : (
            <ul className="max-h-36 space-y-1 overflow-y-auto">
              {jobs.map((item) => (
                <li key={item.id}>
                  <Button
                    type="button"
                    variant="ghost"
                    className="h-auto w-full justify-start whitespace-normal break-all text-left"
                    disabled={busy}
                    onClick={() => void load(item.id)}
                  >
                    {t(prefix + "history_item", {
                      id: item.id,
                      filename: item.filename,
                      status: t(prefix + "status_" + item.status, {
                        defaultValue: t(prefix + "status_unknown"),
                      }),
                    })}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </section>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              pause.current = true;
              controller.current?.abort();
              setPassword("");
              onOpenChange(false);
            }}
          >
            {t(prefix + "close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
