import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ApiRequestError } from "@/lib/api/client";
import {
  createMailboxFolder,
  renameMailboxFolder,
  deleteMailboxFolder,
  MailboxFolder,
} from "./mailbox-api";

export function MailboxFolderDialog({
  open,
  onOpenChange,
  folder,
  uidValidity,
  onChanged,
}: {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  folder?: MailboxFolder | null;
  uidValidity?: number;
  onChanged: (nameEnc?: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [rename, setRename] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (open) {
      setName("");
      setRename(folder?.name ?? "");
      setConfirmed(false);
      setError(null);
    }
  }, [open, folder?.name]);
  const protectedFolder =
    !folder ||
    [
      "inbox",
      "sent",
      "drafts",
      "trash",
      "junk",
      "spam",
      "archive",
      "archives",
    ].includes(folder.name.toLowerCase()) ||
    (folder.flags ?? []).some((flag) =>
      ["\\sent", "\\drafts", "\\trash", "\\junk", "\\archive"].includes(
        flag.toLowerCase(),
      ),
    );
  const perform = async (action: "create" | "rename" | "delete") => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      let next: string | undefined;
      if (action === "create") {
        const result = await createMailboxFolder(name.trim());
        next = result.folder?.name_enc ?? result.name_enc;
        setName("");
      } else if (folder && uidValidity) {
        if (action === "rename") {
          const result = await renameMailboxFolder(
            folder.name_enc,
            rename.trim(),
            uidValidity,
          );
          next = result.folder?.name_enc ?? result.name_enc;
        } else {
          const result = await deleteMailboxFolder(
            folder.name_enc,
            uidValidity,
          );
          next = result.name_enc;
        }
      } else return;
      await onChanged(next);
      if (action !== "create") onOpenChange(false);
    } catch (err) {
      setError(
        err instanceof ApiRequestError
          ? t(`error.${err.code}`, {
              defaultValue: t("page.webmail.folder_action_failed"),
            })
          : t("page.webmail.folder_action_failed"),
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!busy) onOpenChange(value);
      }}
    >
      <DialogContent className="max-w-lg max-h-[90dvh] overflow-auto">
        <DialogHeader>
          <DialogTitle>{t("page.webmail.folder_manage")}</DialogTitle>
        </DialogHeader>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <form
          className="space-y-2"
          onSubmit={(event) => {
            event.preventDefault();
            void perform("create");
          }}
        >
          <label htmlFor="folder-create-name" className="text-sm">
            {t("page.webmail.folder_new_name")}
          </label>
          <Input
            id="folder-create-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            maxLength={200}
            required
            disabled={busy}
          />
          <Button type="submit" disabled={busy || !name.trim()}>
            {t("page.webmail.folder_create")}
          </Button>
        </form>
        <section className="border-t pt-3 space-y-3">
          <h3 className="font-medium break-all">
            {folder?.name ?? t("page.webmail.folders")}
          </h3>
          {protectedFolder ? (
            <p className="text-sm text-muted-foreground">
              {t("page.webmail.folder_protected")}
            </p>
          ) : (
            <>
              <form
                className="space-y-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  void perform("rename");
                }}
              >
                <label htmlFor="folder-rename-name" className="text-sm">
                  {t("page.webmail.folder_rename_name")}
                </label>
                <Input
                  id="folder-rename-name"
                  value={rename}
                  onChange={(event) => setRename(event.target.value)}
                  maxLength={200}
                  required
                  disabled={busy}
                />
                <Button
                  type="submit"
                  variant="outline"
                  disabled={
                    busy ||
                    !uidValidity ||
                    !rename.trim() ||
                    rename === folder?.name
                  }
                >
                  {t("page.webmail.folder_rename")}
                </Button>
              </form>
              <p className="text-sm text-muted-foreground">
                {t("page.webmail.folder_delete_empty_only")}
              </p>
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={confirmed}
                  onChange={(event) => setConfirmed(event.target.checked)}
                  disabled={busy}
                />
                {t("page.webmail.folder_delete_confirm")}
              </label>
              <Button
                type="button"
                variant="destructive"
                disabled={busy || !uidValidity || !confirmed}
                onClick={() => void perform("delete")}
              >
                {t("page.webmail.folder_delete")}
              </Button>
            </>
          )}
        </section>
      </DialogContent>
    </Dialog>
  );
}
