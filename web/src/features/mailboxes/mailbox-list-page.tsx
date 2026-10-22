import { useEffect, useState, useCallback, FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Mail, Plus, Trash2, KeyRound, HardDrive, Power } from "lucide-react";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog, NoticeDialog } from "@/components/ui/notice-dialog";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

interface MailboxItem {
  id: number;
  domain_id: number;
  local_part: string;
  address: string;
  display_name?: { String: string; Valid: boolean };
  quota_bytes: number;
  status: string;
}

interface DomainItem {
  id: number;
  fqdn: string;
}

export function MailboxListPage() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [mailboxes, setMailboxes] = useState<MailboxItem[]>([]);
  const [domains, setDomains] = useState<DomainItem[]>([]);
  const [selectedDomainId, setSelectedDomainId] = useState<number | null>(null);
  const [localPart, setLocalPart] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [isAdding, setIsAdding] = useState(false);

  // Password reset modal state
  const [resetId, setResetId] = useState<number | null>(null);
  const [newPassword, setNewPassword] = useState("");

  // Quota edit state (MB; 0 means unlimited)
  const [quotaId, setQuotaId] = useState<number | null>(null);
  const [quotaMB, setQuotaMB] = useState("");

  // Delete confirmation state
  const [deleteTarget, setDeleteTarget] = useState<MailboxItem | null>(null);

  const [notice, setNotice] = useState<{
    variant: "error" | "info";
    message: string;
  } | null>(null);

  const fetchData = useCallback(() => {
    request<{ mailboxes: MailboxItem[] }>("/api/v1/mailboxes")
      .then((res) => setMailboxes(res.mailboxes || []))
      .catch(() => {});

    request<{ domains: DomainItem[] }>("/api/v1/domains")
      .then((res) => {
        const dList = res.domains || [];
        setDomains(dList);
        setSelectedDomainId((prev) =>
          prev === null && dList.length > 0 ? dList[0].id : prev,
        );
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const toggleStatus = async (m: MailboxItem) => {
    try {
      await request("/api/v1/mailboxes/" + m.id, {
        method: "PATCH",
        body: { status: m.status === "active" ? "disabled" : "active" },
      });
      fetchData();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const handleQuota = async (e: FormEvent) => {
    e.preventDefault();
    if (!quotaId) return;
    const value = Number(quotaMB);
    if (!Number.isFinite(value) || value < 0) return;
    try {
      await request("/api/v1/mailboxes/" + quotaId + "/quota", {
        method: "POST",
        body: { quota_bytes: Math.floor(value * 1024 * 1024) },
      });
      setQuotaId(null);
      setQuotaMB("");
      fetchData();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (!selectedDomainId || !localPart.trim() || !password.trim()) return;
    setIsAdding(true);

    try {
      await request("/api/v1/mailboxes", {
        method: "POST",
        body: {
          domain_id: selectedDomainId,
          local_part: localPart.trim(),
          password: password.trim(),
          display_name: displayName.trim(),
        },
      });

      setLocalPart("");
      setPassword("");
      setDisplayName("");
      fetchData();
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.mailboxes.create_failed")}: ${errorMessage(err)}`,
      });
    } finally {
      setIsAdding(false);
    }
  };

  const handleResetPassword = async (e: FormEvent) => {
    e.preventDefault();
    if (!resetId || !newPassword.trim()) return;

    try {
      await request(`/api/v1/mailboxes/${resetId}/password`, {
        method: "POST",
        body: { password: newPassword.trim() },
      });
      setResetId(null);
      setNewPassword("");
      setNotice({
        variant: "info",
        message: t("page.mailboxes.reset_success"),
      });
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.mailboxes.reset_failed")}: ${errorMessage(err)}`,
      });
    }
  };

  const handleDelete = async (mailbox: MailboxItem) => {
    setDeleteTarget(null);
    try {
      await request(`/api/v1/mailboxes/${mailbox.id}`, { method: "DELETE" });
      fetchData();
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.mailboxes.delete_failed")}: ${errorMessage(err)}`,
      });
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          {t("page.mailboxes.title")}
        </h2>
        <p className="text-sm text-muted-foreground">
          {t("page.mailboxes.subtitle")}
        </p>
      </div>

      {/* Create Mailbox Card */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.mailboxes.create_title")}
          </CardTitle>
          <CardDescription>
            {domains.length === 0
              ? t("page.mailboxes.create_desc_no_domain")
              : t("page.mailboxes.create_desc")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={handleCreate}
            className="grid gap-3 sm:grid-cols-4 items-end"
          >
            <div>
              <label className="text-xs font-medium text-muted-foreground">
                {t("page.mailboxes.field_domain")}
              </label>
              <select
                className="w-full h-9 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm"
                value={selectedDomainId ?? ""}
                onChange={(e) => setSelectedDomainId(Number(e.target.value))}
                disabled={domains.length === 0}
              >
                {domains.map((d) => (
                  <option key={d.id} value={d.id}>
                    @{d.fqdn}
                  </option>
                ))}
              </select>
            </div>

            <div>
              <label className="text-xs font-medium text-muted-foreground">
                {t("page.mailboxes.field_local_part")}
              </label>
              <Input
                placeholder="alice"
                value={localPart}
                onChange={(e) => setLocalPart(e.target.value)}
                required
                disabled={domains.length === 0}
              />
            </div>

            <div>
              <label className="text-xs font-medium text-muted-foreground">
                {t("page.mailboxes.field_password")}
              </label>
              <Input
                type="password"
                placeholder={t("page.mailboxes.password_hint")}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                disabled={domains.length === 0}
              />
            </div>

            <Button type="submit" disabled={isAdding || domains.length === 0}>
              <Plus className="h-4 w-4 mr-1" />
              {isAdding
                ? t("page.mailboxes.creating")
                : t("page.mailboxes.create")}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* Password Reset Modal / Dialog */}
      {resetId && (
        <Card className="border-primary/40 bg-muted/10">
          <CardHeader>
            <CardTitle className="text-sm flex items-center gap-2">
              <KeyRound className="h-4 w-4" />
              {t("page.mailboxes.reset_title", { id: resetId })}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form
              onSubmit={handleResetPassword}
              className="flex gap-3 max-w-sm"
            >
              <Input
                type="password"
                placeholder={t("page.mailboxes.new_password_placeholder")}
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
              />
              <Button type="submit" size="sm">
                {t("page.mailboxes.confirm_reset")}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => setResetId(null)}
              >
                {t("action.cancel")}
              </Button>
            </form>
          </CardContent>
        </Card>
      )}

      {quotaId && (
        <Card className="border-primary/40 bg-muted/10">
          <CardHeader>
            <CardTitle className="text-sm flex items-center gap-2">
              <HardDrive className="h-4 w-4" />
              {t("page.mailboxes.quota_title", { id: quotaId })}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleQuota} className="flex gap-3 max-w-sm">
              <Input
                type="number"
                min="0"
                placeholder={t("page.mailboxes.quota_placeholder")}
                value={quotaMB}
                onChange={(e) => setQuotaMB(e.target.value)}
                required
              />
              <Button type="submit" size="sm">
                {t("page.mailboxes.confirm_quota")}
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => setQuotaId(null)}
              >
                {t("action.cancel")}
              </Button>
            </form>
          </CardContent>
        </Card>
      )}

      {/* Mailbox List Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.mailboxes.list_title")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {mailboxes.length === 0 ? (
              <div className="p-8 text-center text-muted-foreground text-sm">
                {t("page.mailboxes.empty")}
              </div>
            ) : (
              mailboxes.map((m) => (
                <div
                  key={m.id}
                  className="p-4 flex items-center justify-between"
                >
                  <div className="flex items-center gap-3">
                    <Mail className="h-5 w-5 text-muted-foreground" />
                    <div>
                      <div className="font-medium">{m.address}</div>
                      <div className="text-xs text-muted-foreground flex items-center gap-2 mt-0.5">
                        <span className="px-1.5 py-0.5 bg-emerald-500/10 text-emerald-600 rounded">
                          {m.status}
                        </span>
                        <span>
                          {t("page.mailboxes.quota")}:{" "}
                          {m.quota_bytes > 0
                            ? `${(m.quota_bytes / 1024 / 1024).toFixed(0)} MB`
                            : t("page.mailboxes.unlimited")}
                        </span>
                      </div>
                    </div>
                  </div>

                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        setResetId(m.id);
                        setNewPassword("");
                      }}
                    >
                      <KeyRound className="h-3.5 w-3.5 mr-1" />
                      {t("page.mailboxes.change_password")}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        setQuotaId(m.id);
                        setQuotaMB(
                          m.quota_bytes > 0
                            ? String(Math.round(m.quota_bytes / 1024 / 1024))
                            : "0",
                        );
                      }}
                    >
                      <HardDrive className="h-3.5 w-3.5 mr-1" />
                      {t("page.mailboxes.quota")}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => toggleStatus(m)}
                    >
                      <Power className="h-3.5 w-3.5 mr-1" />
                      {m.status === "active"
                        ? t("page.mailboxes.disable")
                        : t("page.mailboxes.enable")}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:bg-destructive/10"
                      onClick={() => setDeleteTarget(m)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={t("page.mailboxes.delete_confirm_title")}
        description={t("page.mailboxes.delete_confirm")}
        confirmLabel={t("action.delete")}
        destructive
        onConfirm={() =>
          deleteTarget ? handleDelete(deleteTarget) : Promise.resolve()
        }
      />

      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice?.message ?? ""}
        variant={notice?.variant}
      />
    </div>
  );
}
