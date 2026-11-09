import { Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";

interface WebmailAICueStripProps {
  onTriggerSummary: () => void;
  isLoading?: boolean;
}

export function WebmailAICueStrip({
  onTriggerSummary,
  isLoading,
}: WebmailAICueStripProps) {
  const { t } = useTranslation();

  return (
    <div
      data-testid="ai-cue-strip"
      className="h-10 px-4 bg-[#F6F4EE] border border-[#EBE8E0] rounded-lg mb-4 flex items-center justify-between"
    >
      <div className="flex items-center gap-2 text-xs text-[#18181B]">
        <span className="text-[#D97706]">
          <Sparkles className="w-4 h-4" />
        </span>
        <span>{t("webmail_next.ai_cue_long_mail")}</span>
      </div>
      <div className="flex items-center gap-2">
        <button
          data-testid="btn-trigger-summary"
          type="button"
          onClick={onTriggerSummary}
          disabled={isLoading}
          className="h-7 px-3 bg-[#2563EB] hover:bg-[#1D4ED8] disabled:opacity-50 text-white text-xs font-semibold rounded-md flex items-center gap-1.5 transition-colors"
        >
          <span>{t("webmail_next.ai_cue_trigger_btn")}</span>
        </button>
      </div>
    </div>
  );
}
