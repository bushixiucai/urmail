import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Sparkles, X, Bot, FileText, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { MailboxFolder, MailboxAICitation } from "./mailbox-api";
import { MailboxAIAsk } from "./mailbox-ai-ask";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import type { MessageDetail } from "./mailbox-api";

interface MailboxAIDrawerProps {
  open: boolean;
  onClose: () => void;
  folders: MailboxFolder[];
  selectedFolderEnc?: string;
  detail: MessageDetail | null;
  onCitation: (source: MailboxAICitation) => void;
  onApplyAIProposal?: (proposal: { subject: string; body: string }) => void;
}

export function MailboxAIDrawer({
  open,
  onClose,
  folders,
  selectedFolderEnc,
  detail,
  onCitation,
  onApplyAIProposal,
}: MailboxAIDrawerProps) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<"current" | "ask">("current");

  if (!open) return null;

  return (
    <aside
      className="w-80 md:w-96 border-l bg-card/95 backdrop-blur-sm flex flex-col h-full shadow-xl z-30 shrink-0 transition-all duration-200"
      aria-label="AI Copilot"
    >
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b bg-muted/30 shrink-0">
        <div className="flex items-center gap-2">
          <div className="h-7 w-7 rounded-lg bg-amber-500/10 text-amber-500 flex items-center justify-center">
            <Sparkles className="h-4 w-4" />
          </div>
          <div>
            <h3 className="text-sm font-semibold">AI Copilot</h3>
            <p className="text-[11px] text-muted-foreground">
              智能邮件助手与检索
            </p>
          </div>
        </div>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0 cursor-pointer"
          onClick={onClose}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>

      {/* Mode Switcher Tabs */}
      <div className="flex border-b px-3 pt-2 gap-1 shrink-0 bg-muted/10">
        <Button
          type="button"
          size="sm"
          variant={activeTab === "current" ? "secondary" : "ghost"}
          className={
            "h-8 text-xs gap-1.5 flex-1 " +
            (activeTab === "current"
              ? "font-semibold bg-background shadow-xs"
              : "text-muted-foreground")
          }
          onClick={() => setActiveTab("current")}
        >
          <FileText className="h-3.5 w-3.5" />
          当前邮件分析
        </Button>
        <Button
          type="button"
          size="sm"
          variant={activeTab === "ask" ? "secondary" : "ghost"}
          className={
            "h-8 text-xs gap-1.5 flex-1 " +
            (activeTab === "ask"
              ? "font-semibold bg-background shadow-xs"
              : "text-muted-foreground")
          }
          onClick={() => setActiveTab("ask")}
        >
          <Search className="h-3.5 w-3.5" />
          全邮箱问答
        </Button>
      </div>

      {/* Body Area */}
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {activeTab === "current" ? (
          detail ? (
            <div className="space-y-4">
              <div className="p-3 rounded-lg bg-muted/40 border border-border/60 space-y-1">
                <p className="text-xs font-medium text-foreground truncate">
                  {detail.subject || t("page.webmail.no_subject")}
                </p>
                <p className="text-[11px] text-muted-foreground truncate">
                  来自: {detail.from?.[0]?.email || "未知发件人"}
                </p>
              </div>

              {detail.uid_validity && selectedFolderEnc && (
                <MailboxAIPanel
                  mode="read"
                  source={{
                    folder_enc: selectedFolderEnc,
                    uid: detail.uid,
                    uid_validity: detail.uid_validity,
                  }}
                  subject={detail.subject}
                  body={detail.text ?? ""}
                  onApply={onApplyAIProposal}
                />
              )}
            </div>
          ) : (
            <div className="h-48 flex flex-col items-center justify-center text-center p-6 text-muted-foreground space-y-2">
              <Bot className="h-8 w-8 stroke-[1.5] opacity-50" />
              <p className="text-xs">
                选择一封邮件即可进行智能摘要、关键待办提取与回复起草
              </p>
            </div>
          )
        ) : (
          <div className="space-y-3">
            <MailboxAIAsk folders={folders} onCitation={onCitation} />
          </div>
        )}
      </div>
    </aside>
  );
}
