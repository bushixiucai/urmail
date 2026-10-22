import { useEffect, useState, useCallback, FormEvent } from "react";
import { Share2, Plus, Trash2 } from "lucide-react";
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
  const [aliases, setAliases] = useState<AliasItem[]>([]);
  const [domains, setDomains] = useState<DomainItem[]>([]);
  const [selectedDomainId, setSelectedDomainId] = useState<number | null>(null);
  const [localPart, setLocalPart] = useState("");
  const [target, setTarget] = useState("");
  const [isCatchall, setIsCatchall] = useState(false);
  const [isAdding, setIsAdding] = useState(false);

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
      alert("创建别名失败: " + getApiErrorMessage(err));
    } finally {
      setIsAdding(false);
    }
  };

  const handleDelete = async (id: number) => {
    if (!confirm("确定要删除此别名吗？")) return;
    try {
      await request(`/api/v1/aliases/${id}`, { method: "DELETE" });
      fetchData();
    } catch (err: unknown) {
      alert("删除失败: " + getApiErrorMessage(err));
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">别名与转发</h2>
        <p className="text-sm text-muted-foreground">
          支持邮件地址重定向（例如 info@ → alice@）与域名 Catchall 通配收信
        </p>
      </div>

      {/* Create Alias Card */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">新建别名规则</CardTitle>
          <CardDescription>
            设置别名前缀与目标收信地址（支持转发至站内邮箱或外部 Gmail/QQ 邮箱）
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={handleCreate}
            className="grid gap-3 sm:grid-cols-4 items-end"
          >
            <div>
              <label className="text-xs font-medium text-muted-foreground">
                所属域名
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
                  别名前缀
                </label>
                <label className="text-[10px] text-muted-foreground flex items-center gap-1 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={isCatchall}
                    onChange={(e) => setIsCatchall(e.target.checked)}
                  />
                  Catchall 全域通配
                </label>
              </div>
              <Input
                placeholder={isCatchall ? "@ (全部未匹配邮件)" : "contact"}
                value={isCatchall ? "" : localPart}
                onChange={(e) => setLocalPart(e.target.value)}
                disabled={isCatchall || domains.length === 0}
                required={!isCatchall}
              />
            </div>

            <div>
              <label className="text-xs font-medium text-muted-foreground">
                转发目标地址
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
              {isAdding ? "添加中..." : "添加别名"}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* Aliases List Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">规则列表</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {aliases.length === 0 ? (
              <div className="p-8 text-center text-muted-foreground text-sm">
                暂无别名规则
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
                    onClick={() => handleDelete(a.id)}
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
