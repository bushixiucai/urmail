import { useEffect, useState, useCallback, useRef, FormEvent } from "react";
import { useTranslation } from "react-i18next";
import {
  Globe,
  Plus,
  Trash2,
  CheckCircle,
  Copy,
  Check,
  Power,
} from "lucide-react";
import { useAuth } from "@/app/auth-context";
import { DomainAuthorityPanel } from "./domain-authority-panel";
import { DomainTransportPanel } from "./domain-transport-panel";
import { DomainDNSPanel } from "./domain-dns-panel";
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

interface DomainItem {
  id: number;
  fqdn: string;
  mode: string;
  status: string;
  created_at: string;
  provisioning_state?: string;
}

interface DNSGuidance {
  mx: string;
  spf: string;
  dkim?: string;
  dmarc: string;
  verification_name?: string;
  verification_value?: string;
}

export function DomainListPage() {
  const { t } = useTranslation();
  const { currentTenant } = useAuth();
  const canManage =
    currentTenant?.role === "owner" || currentTenant?.role === "admin";
  const [selectedDNS, setSelectedDNS] = useState<number | null>(null);
  const [mode, setMode] = useState("delegated");
  const errorMessage = useApiErrorMessage();
  const domainFetch = useRef({ version: 0 });
  const [domains, setDomains] = useState<DomainItem[]>([]);
  const [newFqdn, setNewFqdn] = useState("");
  const [dnsGuidance, setDnsGuidance] = useState<DNSGuidance | null>(null);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [isAdding, setIsAdding] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<DomainItem | null>(null);
  const [notice, setNotice] = useState<{
    variant: "error" | "info";
    message: string;
  } | null>(null);

  useEffect(() => {
    setSelectedDNS(null);
    setDnsGuidance(null);
    setDomains([]);
  }, [currentTenant?.id]);

  const fetchDomains = useCallback(() => {
    if (!currentTenant?.id) {
      setDomains([]);
      return;
    }
    const version = ++domainFetch.current.version;
    request<{ domains: DomainItem[] }>("/api/v1/domains")
      .then((res) => {
        if (version === domainFetch.current.version)
          setDomains(res.domains || []);
      })
      .catch(() => {});
  }, [currentTenant?.id]);

  useEffect(() => {
    const counter = domainFetch.current;
    fetchDomains();
    return () => {
      counter.version++;
    };
  }, [fetchDomains]);

  const handleAddDomain = async (e: FormEvent) => {
    e.preventDefault();
    if (!newFqdn.trim()) return;
    setIsAdding(true);

    try {
      const res = await request<{
        domain: DomainItem;
        dns_guidance: DNSGuidance;
      }>("/api/v1/domains", {
        method: "POST",
        body: { fqdn: newFqdn.trim(), mode },
      });
      setNewFqdn("");
      setDnsGuidance(res.dns_guidance);
      fetchDomains();
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.domains.add_failed")}: ${errorMessage(err)}`,
      });
    } finally {
      setIsAdding(false);
    }
  };

  const handleVerify = async (domain: DomainItem) => {
    try {
      await request("/api/v1/domains/" + domain.id + "/verify", {
        method: "POST",
      });
      fetchDomains();
      setNotice({
        variant: "info",
        message: t("page.domains.verify_success"),
      });
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: t("page.domains.verify_failed") + ": " + errorMessage(err),
      });
    }
  };

  const handleToggleStatus = async (domain: DomainItem) => {
    try {
      await request("/api/v1/domains/" + domain.id, {
        method: "PATCH",
        body: { status: domain.status === "disabled" ? "active" : "disabled" },
      });
      fetchDomains();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const handleDelete = async (domain: DomainItem) => {
    setDeleteTarget(null);
    try {
      await request(`/api/v1/domains/${domain.id}`, { method: "DELETE" });
      fetchDomains();
    } catch (err: unknown) {
      setNotice({
        variant: "error",
        message: `${t("page.domains.delete_failed")}: ${errorMessage(err)}`,
      });
    }
  };

  const copyToClipboard = (text: string, key: string) => {
    navigator.clipboard.writeText(text);
    setCopiedKey(key);
    setTimeout(() => setCopiedKey(null), 2000);
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">
            {t("page.domains.title")}
          </h2>
          <p className="text-sm text-muted-foreground">
            {t("page.domains.subtitle")}
          </p>
        </div>
      </div>

      {/* Add Domain Form */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.domains.add_title")}
          </CardTitle>
          <CardDescription>{t("page.domains.add_desc")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={handleAddDomain}
            className="flex flex-wrap gap-3 max-w-xl"
          >
            <Input
              placeholder="example.com"
              value={newFqdn}
              onChange={(e) => setNewFqdn(e.target.value)}
              required
            />
            <select
              className="h-10 rounded-md border border-input bg-background px-3 text-sm"
              aria-label={t("page.dns.mode")}
              value={mode}
              onChange={(e) => setMode(e.target.value)}
            >
              <option value="delegated">{t("page.dns.delegated")}</option>
              <option value="managed">{t("page.dns.managed")}</option>
            </select>
            <Button type="submit" disabled={isAdding}>
              <Plus className="h-4 w-4 mr-1" />
              {isAdding ? t("page.domains.adding") : t("page.domains.add")}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* DNS Guidance Box (if present) */}
      {dnsGuidance && (
        <Card className="border-primary/20 bg-primary/5">
          <CardHeader>
            <div className="flex max-w-full flex-wrap items-center gap-2 lg:shrink-0">
              <CheckCircle className="h-5 w-5 text-emerald-500" />
              <CardTitle className="text-base">
                {t("page.domains.dns_title")}
              </CardTitle>
            </div>
            <CardDescription>{t("page.domains.dns_desc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3 font-mono text-xs">
            <div className="p-3 bg-card border rounded flex items-center justify-between">
              <div>
                <span className="font-bold text-foreground">MX: </span>
                <span>{dnsGuidance.mx}</span>
              </div>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => copyToClipboard(dnsGuidance.mx, "mx")}
              >
                {copiedKey === "mx" ? (
                  <Check className="h-3 w-3" />
                ) : (
                  <Copy className="h-3 w-3" />
                )}
              </Button>
            </div>

            <div className="p-3 bg-card border rounded flex items-center justify-between">
              <div>
                <span className="font-bold text-foreground">SPF (TXT @): </span>
                <span>{dnsGuidance.spf}</span>
              </div>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => copyToClipboard(dnsGuidance.spf, "spf")}
              >
                {copiedKey === "spf" ? (
                  <Check className="h-3 w-3" />
                ) : (
                  <Copy className="h-3 w-3" />
                )}
              </Button>
            </div>

            {dnsGuidance.dkim && (
              <div className="p-3 bg-card border rounded flex items-center justify-between">
                <div className="overflow-hidden">
                  <span className="font-bold text-foreground">
                    DKIM (TXT mail._domainkey):{" "}
                  </span>
                  <span className="truncate block max-w-xl">
                    {dnsGuidance.dkim}
                  </span>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => copyToClipboard(dnsGuidance.dkim!, "dkim")}
                >
                  {copiedKey === "dkim" ? (
                    <Check className="h-3 w-3" />
                  ) : (
                    <Copy className="h-3 w-3" />
                  )}
                </Button>
              </div>
            )}

            {dnsGuidance.verification_name &&
              dnsGuidance.verification_value && (
                <div className="p-3 bg-card border rounded flex items-center justify-between">
                  <div className="overflow-hidden">
                    <span className="font-bold text-foreground">
                      {t("page.domains.verification_txt")}:{" "}
                    </span>
                    <span className="break-all">
                      {dnsGuidance.verification_name} ={" "}
                      {dnsGuidance.verification_value}
                    </span>
                  </div>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() =>
                      copyToClipboard(
                        dnsGuidance.verification_value!,
                        "verification",
                      )
                    }
                  >
                    {copiedKey === "verification" ? (
                      <Check className="h-3 w-3" />
                    ) : (
                      <Copy className="h-3 w-3" />
                    )}
                  </Button>
                </div>
              )}
          </CardContent>
        </Card>
      )}

      {/* Domain List Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("page.domains.bound")}</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {domains.length === 0 ? (
              <div className="p-8 text-center text-muted-foreground text-sm">
                {t("page.domains.empty")}
              </div>
            ) : (
              domains.map((d) => (
                <div
                  key={d.id}
                  data-testid={`domain-row-${d.id}`}
                  className="p-4 flex flex-col items-start gap-3 lg:flex-row lg:items-center lg:justify-between"
                >
                  <div className="flex min-w-0 max-w-full items-center gap-3">
                    <Globe className="h-5 w-5 text-muted-foreground" />
                    <div className="min-w-0">
                      <div className="font-medium break-all">{d.fqdn}</div>
                      <p className="text-xs text-muted-foreground">
                        {t(
                          `page.dns.provision.${d.provisioning_state ?? "unknown"}`,
                        )}
                      </p>
                      <div className="text-xs text-muted-foreground flex flex-wrap items-center gap-2 mt-0.5">
                        <span className="px-1.5 py-0.5 bg-emerald-500/10 text-emerald-600 rounded">
                          {t(`status.${d.status}`)}
                        </span>
                        <span>
                          {t("page.domains.mode")}: {t(`status.${d.mode}`)}
                        </span>
                      </div>
                    </div>
                  </div>

                  <div className="flex max-w-full flex-wrap items-center gap-2 lg:shrink-0">
                    {d.status !== "active" && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => handleVerify(d)}
                      >
                        <CheckCircle className="h-3.5 w-3.5 mr-1" />
                        {t("page.domains.verify")}
                      </Button>
                    )}
                    {canManage && ["active", "disabled"].includes(d.status) && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() =>
                          setSelectedDNS(selectedDNS === d.id ? null : d.id)
                        }
                      >
                        {t("page.dns.open")}
                      </Button>
                    )}
                    {(d.status === "active" || d.status === "disabled") && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => handleToggleStatus(d)}
                      >
                        <Power className="h-3.5 w-3.5 mr-1" />
                        {d.status === "active"
                          ? t("page.domains.disable")
                          : t("page.domains.enable")}
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={t("action.delete")}
                      className="text-destructive hover:bg-destructive/10"
                      onClick={() => setDeleteTarget(d)}
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

      {canManage &&
        domains
          .filter((d) => d.id === selectedDNS)
          .map((d) => (
            <div
              key={`${currentTenant?.id}:${d.id}:${d.mode}`}
              className="space-y-4"
            >
              <DomainDNSPanel domain={d} />
              <DomainAuthorityPanel domain={d} />
              <DomainTransportPanel domain={d} />
            </div>
          ))}

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={t("page.domains.delete_confirm_title")}
        description={t("page.domains.delete_confirm")}
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
