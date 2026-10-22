import { useEffect, useState, useCallback, FormEvent } from "react";
import { Globe, Plus, Trash2, CheckCircle, Copy, Check } from "lucide-react";
import { request, getApiErrorMessage } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
}

interface DNSGuidance {
  mx: string;
  spf: string;
  dkim?: string;
  dmarc: string;
}

export function DomainListPage() {
  const [domains, setDomains] = useState<DomainItem[]>([]);
  const [newFqdn, setNewFqdn] = useState("");
  const [dnsGuidance, setDnsGuidance] = useState<DNSGuidance | null>(null);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [isAdding, setIsAdding] = useState(false);

  const fetchDomains = useCallback(() => {
    request<{ domains: DomainItem[] }>("/api/v1/domains")
      .then((res) => setDomains(res.domains || []))
      .catch(() => {});
  }, []);

  useEffect(() => {
    fetchDomains();
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
        body: { fqdn: newFqdn.trim() },
      });
      setNewFqdn("");
      setDnsGuidance(res.dns_guidance);
      fetchDomains();
    } catch (err: unknown) {
      alert("添加域名失败: " + getApiErrorMessage(err));
    } finally {
      setIsAdding(false);
    }
  };

  const handleDelete = async (id: number) => {
    if (!confirm("确定要删除此域名吗？")) return;
    try {
      await request(`/api/v1/domains/${id}`, { method: "DELETE" });
      fetchDomains();
    } catch (err: unknown) {
      alert("删除失败: " + getApiErrorMessage(err));
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
          <h2 className="text-2xl font-bold tracking-tight">域名管理</h2>
          <p className="text-sm text-muted-foreground">
            管理租户邮箱域名，查看 DNS 指引并自动签发证书
          </p>
        </div>
      </div>

      {/* Add Domain Form */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">添加新域名</CardTitle>
          <CardDescription>
            输入您的自有域名（例如 example.com）
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleAddDomain} className="flex gap-3 max-w-md">
            <Input
              placeholder="example.com"
              value={newFqdn}
              onChange={(e) => setNewFqdn(e.target.value)}
              required
            />
            <Button type="submit" disabled={isAdding}>
              <Plus className="h-4 w-4 mr-1" />
              {isAdding ? "添加中..." : "添加域名"}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* DNS Guidance Box (if present) */}
      {dnsGuidance && (
        <Card className="border-primary/20 bg-primary/5">
          <CardHeader>
            <div className="flex items-center gap-2">
              <CheckCircle className="h-5 w-5 text-emerald-500" />
              <CardTitle className="text-base">
                域名已添加！请配置以下 DNS 解析记录
              </CardTitle>
            </div>
            <CardDescription>
              前往您的域名注册商（如 Cloudflare,
              阿里云）添加下列记录以完成邮件投递与 SPF/DKIM 认证
            </CardDescription>
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
          </CardContent>
        </Card>
      )}

      {/* Domain List Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">已绑定域名</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {domains.length === 0 ? (
              <div className="p-8 text-center text-muted-foreground text-sm">
                暂无域名，请先添加域名
              </div>
            ) : (
              domains.map((d) => (
                <div
                  key={d.id}
                  className="p-4 flex items-center justify-between"
                >
                  <div className="flex items-center gap-3">
                    <Globe className="h-5 w-5 text-muted-foreground" />
                    <div>
                      <div className="font-medium">{d.fqdn}</div>
                      <div className="text-xs text-muted-foreground flex items-center gap-2 mt-0.5">
                        <span className="px-1.5 py-0.5 bg-emerald-500/10 text-emerald-600 rounded">
                          {d.status}
                        </span>
                        <span>模式: {d.mode}</span>
                      </div>
                    </div>
                  </div>

                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive hover:bg-destructive/10"
                    onClick={() => handleDelete(d.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
