import { useEffect, useState, useCallback, FormEvent } from "react";
import { Mail, Plus, Trash2, KeyRound } from "lucide-react";
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
      alert("创建邮箱失败: " + getApiErrorMessage(err));
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
      alert("密码已成功修改");
    } catch (err: unknown) {
      alert("修改失败: " + getApiErrorMessage(err));
    }
  };

  const handleDelete = async (id: number) => {
    if (!confirm("确定要删除此邮箱账号吗？")) return;
    try {
      await request(`/api/v1/mailboxes/${id}`, { method: "DELETE" });
      fetchData();
    } catch (err: unknown) {
      alert("删除失败: " + getApiErrorMessage(err));
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">邮箱账号</h2>
        <p className="text-sm text-muted-foreground">
          创建与管理租户邮箱用户，支持设置 SHA512-Crypt 密码及单号配额
        </p>
      </div>

      {/* Create Mailbox Card */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">开通新邮箱</CardTitle>
          <CardDescription>
            {domains.length === 0
              ? "请先在「域名管理」添加可用域名"
              : "选择域名并输入前缀与初始密码"}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={handleCreate}
            className="grid gap-3 sm:grid-cols-4 items-end"
          >
            <div>
              <label className="text-xs font-medium text-muted-foreground">
                域名
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
                邮箱前缀
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
                初始密码
              </label>
              <Input
                type="password"
                placeholder="至少 6 位"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                disabled={domains.length === 0}
              />
            </div>

            <Button type="submit" disabled={isAdding || domains.length === 0}>
              <Plus className="h-4 w-4 mr-1" />
              {isAdding ? "开通中..." : "开通账号"}
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
              重置邮箱密码 (ID: {resetId})
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form
              onSubmit={handleResetPassword}
              className="flex gap-3 max-w-sm"
            >
              <Input
                type="password"
                placeholder="输入新密码"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
              />
              <Button type="submit" size="sm">
                确认重置
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => setResetId(null)}
              >
                取消
              </Button>
            </form>
          </CardContent>
        </Card>
      )}

      {/* Mailbox List Table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">邮箱用户列表</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {mailboxes.length === 0 ? (
              <div className="p-8 text-center text-muted-foreground text-sm">
                暂无邮箱账号
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
                          配额:{" "}
                          {m.quota_bytes > 0
                            ? `${(m.quota_bytes / 1024 / 1024).toFixed(0)} MB`
                            : "无限制"}
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
                      修改密码
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:bg-destructive/10"
                      onClick={() => handleDelete(m.id)}
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
    </div>
  );
}
