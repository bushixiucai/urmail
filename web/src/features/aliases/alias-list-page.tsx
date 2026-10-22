import { useEffect, useState, useCallback, FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Share2, Plus, Trash2 } from "lucide-react";
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

interface AliasItem {
  id: number;
  domain_id: number;
  domain_fqdn: string;
  local_part: string;
  target: string;
  kind: string;
  active: boolean;
}

interface DomainItem {
  id: number;
  fqdn: string;
}

export function AliasListPage() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [aliases, setAliases] = useState<AliasItem[]>([]);
  const [domains, setDomains] = useState<DomainItem[]>([]);
  const [selectedDomainId, setSelectedDomainId] = useState<number | null>(null);
  const [localPart, setLocalPart] = useState("");
  const [target, setTarget] = useState("");
  const [isCatchall, setIsCatchall] = useState(false);
  const [isAdding, setIsAdding] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AliasItem | null>(null);
  const [notice, setNotice] = useState<{
    variant: "error" | "info";
    message: string;
  } | null>(null);

  const fetchData = useCallback(() => {
    request<{ aliases: AliasItem[] }>("/api/v1/aliases")
      .then((res) => setAliases(res.aliases || []))
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

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault();
    if (!selectedDomainId || !target.trim()) return;
    if (!isCatchall && !localPart.trim()) return;

    setIsAdding(true);
    try {
      await request("/api/v1/aliases", {
        method: "POST",
        body: {
          domain_id: selectedDomainId,
          local_part: isCatchall ? "" : localPart.trim(),
          target: target.trim(),
          kind: isCatchall ? "catchall" : "alias",
        },
      });

      setLocalPart("");
      setTarget("");
      setIsCatchall(false);
      fetchData();
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.aliases.create_failed")}: ${errorMessage(err)}`,
      });
    } finally {
      setIsAdding(false);
    }
  };

  const handleDelete = async (alias: AliasItem) => {
    setDeleteTarget(null);
    try {
      await request(`/api/v1/aliases/${alias.id}`, { method: "DELETE" });
      fetchData();
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.aliases.delete_failed")}: ${errorMessage(err)}`,
      });
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          {t("page.aliases.title")}
        </h2>
        <p className="text-sm text-muted-foreground">
          {t("page.aliases.subtitle")}
        </p>
      </div>

      {/* Create Alias Card */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.aliases.create_title")}
          </CardTitle>
          <CardDescription>{t("page.aliases.create_desc")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={handleCreate}
            className="grid gap-3 sm:grid-cols-4 items-end"
          >
            <div>
              <label className="text-xs font-medium text-muted-foreground">
                {t("page.aliases.field_domain")}
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
              <div className="flex items-center justify-between">
                <label className="text-xs font-medium text-muted-foreground">
                  {t("page.aliases.field_local_part")}
                </label>
                <label className="text-[10px] text-muted-foreground flex items-center gap-1 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={isCatchall}
                    onChange={(e) => setIsCatchall(e.target.checked)}
                  />
                  {t("page.aliases.catchall")}
                </label>
              </div>
              <Input
                placeholder={
                  isCatchall
                    ? t("page.aliases.catchall_placeholder")
                    : "contact"
                }
                value={isCatchall ? "" : localPart}
                onChange={(e) => setLocalPart(e.target.value)}
                disabled={isCatchall || domains.length === 0}
                required={!isCatchall}
              />
            </div>

            <div>
              <label className="text-xs font-medium text-muted-foreground">
                {t("page.aliases.field_target")}
              </label>
              <Input
                type="email"
                placeholder="target@example.com"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                required
                disabled={domains.length === 0}
              />
            </div>

            <Button type="submit" disabled={isAdding || domains.length === 0}>
              <Plus className="h-4 w-4 mr-1" />
              {isAdding ? t("page.aliases.adding") : t("page.aliases.add")}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* Aliases List Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.aliases.list_title")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {aliases.length === 0 ? (
              <div className="p-8 text-center text-muted-foreground text-sm">
                {t("page.aliases.empty")}
              </div>
            ) : (
              aliases.map((a) => (
                <div
                  key={a.id}
                  className="p-4 flex items-center justify-between"
                >
                  <div className="flex items-center gap-3">
                    <Share2 className="h-5 w-5 text-muted-foreground" />
                    <div>
                      <div className="font-medium flex items-center gap-2">
                        {a.local_part
                          ? `${a.local_part}@${a.domain_fqdn}`
                          : `@${a.domain_fqdn} (Catchall)`}
                        <span className="text-xs text-muted-foreground">→</span>
                        <span className="text-sm font-semibold text-primary">
                          {a.target}
                        </span>
                      </div>
                      <div className="text-xs text-muted-foreground flex items-center gap-2 mt-0.5">
                        <span className="px-1.5 py-0.5 bg-blue-500/10 text-blue-600 rounded">
                          {a.kind}
                        </span>
                      </div>
                    </div>
                  </div>

                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive hover:bg-destructive/10"
                    onClick={() => setDeleteTarget(a)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={t("page.aliases.delete_confirm_title")}
        description={t("page.aliases.delete_confirm")}
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
