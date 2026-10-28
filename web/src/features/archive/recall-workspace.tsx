import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/notice-dialog";

type Copy = { key: string; folder: string; uid: number; uid_validity: number };
type ItemResult = { key: string; status: string; reason?: string };
type Recall = {
  id: number;
  archive_entry_id: number | null;
  status: string;
  reason: string | null;
  preview_token: string | null;
  preview_digest: string | null;
  preview_expires_at: string | null;
  selected_copy_keys: string[];
  local_result: { items?: ItemResult[] };
  attempt_epoch: number;
};
type Target = {
  entry_id: number;
  address: string | null;
  reason: string;
  copies: Copy[];
  recall: Recall | null;
};
type Entry = {
  id: number;
  subject: string | null;
  message_id: string;
  legal_hold: boolean;
};

/** Local recall is a separately reviewed mailbox mutation, never archive deletion. */
export function RecallWorkspace({ entries }: { entries: Entry[] }) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const reasonLabel = (reason: string) =>
    t(
      `recall.reason.${reason.includes("RESTORE_RECONCILIATION_REQUIRED") ? "restore_required" : reason}`,
    );
  const [entryID, setEntryID] = useState(0);
  const [related, setRelated] = useState(true);
  const [targets, setTargets] = useState<Target[]>([]);
  const [selection, setSelection] = useState<Record<number, string[]>>({});
  const [history, setHistory] = useState<Recall[]>([]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const lifetime = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const readHistory = async (signal?: AbortSignal) => {
    const result = await request<{ recalls: Recall[] }>(
      "/api/v1/compliance/recalls",
      { signal },
    );
    return result.recalls ?? [];
  };
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    void readHistory(controller.signal)
      .then((rows) => {
        if (!controller.signal.aborted) setHistory(rows);
      })
      .catch(() => {});
    const activeRequest = lifetime;
    return () => {
      controller.abort();
      activeRequest.current?.abort();
    };
  }, []);
  const action = async (run: (signal: AbortSignal) => Promise<void>) => {
    if (busy) return;
    const current = ++generation.current;
    setBusy(true);
    setNotice("");
    const controller = new AbortController();
    lifetime.current?.abort();
    lifetime.current = controller;
    try {
      await run(controller.signal);
    } catch (error) {
      if (!controller.signal.aborted) setNotice(errorMessage(error));
    } finally {
      if (!controller.signal.aborted && generation.current === current)
        setBusy(false);
    }
  };
  const preview = () =>
    action(async (signal) => {
      setTargets([]);
      setSelection({});
      setConfirmOpen(false);
      const result = await request<{ targets: Target[] }>(
        "/api/v1/compliance/recalls/preview",
        {
          method: "POST",
          body: { archive_entry_id: entryID, include_related: related },
          signal,
        },
      );
      if (signal.aborted) return;
      setTargets(result.targets ?? []);
      setHistory(await readHistory(signal));
    });
  const confirmations = targets
    .filter(
      (target) =>
        target.recall?.status === "preview" &&
        (selection[target.recall.id]?.length ?? 0) > 0,
    )
    .map((target) => ({
      id: target.recall!.id,
      preview_token: target.recall!.preview_token,
      digest: target.recall!.preview_digest,
      copy_keys: selection[target.recall!.id],
    }));
  const submit = async (targetsToConfirm: typeof confirmations) =>
    action(async (signal) => {
      setConfirmOpen(false);
      const result = await request<{ recalls: Recall[] }>(
        "/api/v1/compliance/recalls",
        {
          method: "POST",
          body: { confirmed: true, targets: targetsToConfirm },
          signal,
        },
      );
      if (signal.aborted) return;
      const updated = new Map(result.recalls.map((row) => [row.id, row]));
      setTargets((current) =>
        current.map((target) => ({
          ...target,
          recall: target.recall
            ? (updated.get(target.recall.id) ?? target.recall)
            : null,
        })),
      );
      setSelection({});
      setHistory(await readHistory(signal));
      setNotice(t("recall.result_recorded"));
    });
  return (
    <section
      className="space-y-4 rounded-lg border p-4"
      aria-label={t("recall.title")}
    >
      <header className="space-y-2">
        <h2 className="text-lg font-semibold">{t("recall.title")}</h2>
        <p className="text-sm text-muted-foreground">{t("recall.scope")}</p>
      </header>
      {notice && (
        <p role="status" className="text-sm break-words">
          {notice}
        </p>
      )}
      <div className="flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm min-w-0 flex-1">
          {t("recall.source")}
          <select
            value={entryID}
            disabled={busy}
            className="w-full min-w-0 rounded-md border bg-background px-3 py-2"
            onChange={(event) => {
              setEntryID(Number(event.target.value));
              setTargets([]);
              setSelection({});
              setNotice("");
              setConfirmOpen(false);
            }}
          >
            <option value={0}>{t("recall.choose_source")}</option>
            {entries.map((entry) => (
              <option key={entry.id} value={entry.id}>
                #{entry.id} {entry.subject || entry.message_id}
                {entry.legal_hold ? ` · ${t("archive.held")}` : ""}
              </option>
            ))}
          </select>
        </label>
        <Button
          variant="outline"
          disabled={busy || !entryID}
          onClick={() => void preview()}
        >
          {t("recall.preview")}
        </Button>
        <Button
          variant="ghost"
          disabled={busy}
          onClick={() =>
            void action(async (signal) => setHistory(await readHistory(signal)))
          }
        >
          {t("recall.refresh")}
        </Button>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={related}
          disabled={busy}
          onChange={(event) => {
            setRelated(event.target.checked);
            setTargets([]);
            setSelection({});
          }}
        />
        {t("recall.related")}
      </label>
      <p className="text-xs text-muted-foreground">
        {t("recall.preview_help")}
      </p>
      {targets.map((target) => (
        <article className="space-y-2 rounded border p-3" key={target.entry_id}>
          <h3 className="font-medium break-all">
            #{target.entry_id} {target.address || t("recall.remote")}
          </h3>
          {target.reason && (
            <p className="text-sm">{reasonLabel(target.reason)}</p>
          )}
          {target.recall && (
            <p className="text-xs text-muted-foreground">
              {t("recall.attempt", {
                id: target.recall.id,
                epoch: target.recall.attempt_epoch,
              })}{" "}
              · {t(`recall.status.${target.recall.status}`)}
            </p>
          )}
          {target.recall?.status === "preview" &&
            target.recall.preview_expires_at && (
              <p className="text-xs text-muted-foreground">
                {t("recall.expires", {
                  time: new Date(
                    target.recall.preview_expires_at,
                  ).toLocaleTimeString(),
                })}
              </p>
            )}
          {target.copies.length > 0 && (
            <ul className="space-y-2">
              {target.copies.map((copy) => (
                <li key={copy.key}>
                  <label className="flex items-start gap-2 text-sm">
                    <input
                      type="checkbox"
                      className="mt-1"
                      disabled={
                        busy ||
                        !!target.reason ||
                        target.recall?.status !== "preview"
                      }
                      checked={
                        !!target.recall &&
                        (selection[target.recall.id] ?? []).includes(copy.key)
                      }
                      onChange={(event) => {
                        const id = target.recall!.id;
                        setSelection((current) => ({
                          ...current,
                          [id]: event.target.checked
                            ? [...(current[id] ?? []), copy.key]
                            : (current[id] ?? []).filter(
                                (key) => key !== copy.key,
                              ),
                        }));
                      }}
                    />
                    <span className="break-all">
                      {copy.folder} ·{" "}
                      {t("recall.uid", {
                        uid: copy.uid,
                        validity: copy.uid_validity,
                      })}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          )}
          {target.recall?.local_result?.items?.map((item) => (
            <p key={item.key} className="text-sm break-all">
              {t(`recall.item.${item.status}`)} ·{" "}
              {reasonLabel(item.reason ?? "local_outcome_unknown")}
            </p>
          ))}
          {target.recall?.status === "pending" &&
            target.recall.selected_copy_keys.length > 0 &&
            !target.reason?.includes("other_actor") && (
              <Button
                variant="outline"
                disabled={busy}
                onClick={() =>
                  void submit([
                    {
                      id: target.recall!.id,
                      preview_token: target.recall!.preview_token,
                      digest: target.recall!.preview_digest,
                      copy_keys: target.recall!.selected_copy_keys,
                    },
                  ])
                }
              >
                {t("recall.resume_exact")}
              </Button>
            )}
        </article>
      ))}
      {targets.length > 0 && (
        <Button
          variant="destructive"
          disabled={busy || confirmations.length === 0}
          onClick={() => setConfirmOpen(true)}
        >
          {t("recall.review_selection")}
        </Button>
      )}
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t("recall.confirm_title")}
        description={t("recall.confirm_body")}
        confirmLabel={t("recall.confirm")}
        destructive
        onConfirm={() => submit(confirmations)}
      />
      <details>
        <summary className="cursor-pointer text-sm font-medium">
          {t("recall.history")}
        </summary>
        <p className="mt-2 text-xs text-muted-foreground">
          {t("recall.retry_help")}
        </p>
        {history.length === 0 ? (
          <p className="mt-2 text-sm">{t("recall.history_empty")}</p>
        ) : (
          <ul className="mt-3 divide-y">
            {history.map((row) => (
              <li className="space-y-1 py-3 text-sm" key={row.id}>
                <p>
                  {t("recall.attempt", {
                    id: row.id,
                    epoch: row.attempt_epoch,
                  })}{" "}
                  · {t(`recall.status.${row.status}`)}
                </p>
                {row.reason && (
                  <p className="text-muted-foreground">
                    {reasonLabel(row.reason)}
                  </p>
                )}
                {row.local_result?.items?.map((item) => (
                  <p key={item.key} className="text-xs break-all">
                    {t(`recall.item.${item.status}`)} ·{" "}
                    {reasonLabel(item.reason ?? "local_outcome_unknown")}
                  </p>
                ))}
              </li>
            ))}
          </ul>
        )}
      </details>
    </section>
  );
}
