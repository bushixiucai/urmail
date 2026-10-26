import { validNewPassword } from "@/lib/password-policy";
import { FormEvent, useState } from "react";
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
import { changeMailboxPassword, clearMailboxSession } from "./mailbox-api";

interface MailboxPasswordDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onReauthRequired: () => void;
}

export function MailboxPasswordDialog({
  open,
  onOpenChange,
  onReauthRequired,
}: MailboxPasswordDialogProps) {
  const { t } = useTranslation();
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reset = () => {
    setOldPassword("");
    setNewPassword("");
    setConfirm("");
    setError(null);
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (newPassword !== confirm) {
      setError(t("page.webmail.password_mismatch"));
      return;
    }
    if (!validNewPassword(newPassword)) {
      setError(t("error.PASSWORD_POLICY_INVALID"));
      return;
    }
    setBusy(true);
    try {
      await changeMailboxPassword(oldPassword, newPassword);
      clearMailboxSession();
      onOpenChange(false);
      reset();
      onReauthRequired();
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.password_failed"),
          }),
        );
      } else {
        setError(t("page.webmail.password_failed"));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) reset();
        onOpenChange(next);
      }}
    >
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("page.webmail.password_title")}</DialogTitle>
          <DialogDescription>
            {t("page.webmail.password_desc")}
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-3" onSubmit={onSubmit}>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <div className="space-y-1.5">
            <label htmlFor="mbox-old-pw" className="text-sm font-medium">
              {t("page.webmail.password_old")}
            </label>
            <Input
              id="mbox-old-pw"
              type="password"
              autoComplete="current-password"
              required
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="mbox-new-pw" className="text-sm font-medium">
              {t("page.webmail.password_new")}
            </label>
            <Input
              id="mbox-new-pw"
              type="password"
              autoComplete="new-password"
              required
              minLength={12}
              maxLength={256}
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="mbox-confirm-pw" className="text-sm font-medium">
              {t("page.webmail.password_confirm")}
            </label>
            <Input
              id="mbox-confirm-pw"
              type="password"
              autoComplete="new-password"
              required
              minLength={12}
              maxLength={256}
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={busy}
            >
              {t("action.cancel")}
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? t("state.loading") : t("page.webmail.password_submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
