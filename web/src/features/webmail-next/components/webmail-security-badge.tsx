import { useState } from "react";
import { ShieldCheck, ShieldAlert, Shield } from "lucide-react";
import { useTranslation } from "react-i18next";

interface WebmailSecurityBadgeProps {
  authentication?: {
    status: "pass" | "fail" | "unknown";
    source?: string;
    reason: string;
    domain?: string;
    checked_at?: string;
  };
}

export function WebmailSecurityBadge({
  authentication,
}: WebmailSecurityBadgeProps) {
  const { t } = useTranslation();
  const [showDetails, setShowDetails] = useState(false);

  if (!authentication) return null;

  const isPass = authentication.status === "pass";
  const isFail = authentication.status === "fail";

  return (
    <div className="relative inline-block">
      <button
        data-testid="security-badge-button"
        type="button"
        onClick={() => setShowDetails((p) => !p)}
        className={`flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] font-medium transition-colors ${
          isPass
            ? "bg-[#DCFCE7] text-[#166534] hover:bg-[#BBF7D0]"
            : isFail
              ? "bg-[#FEF3C7] text-[#92400E] hover:bg-[#FDE68A]"
              : "bg-[#F3F4F6] text-[#4B5563]"
        }`}
        title="Security & Authentication Details"
      >
        {isPass ? (
          <ShieldCheck className="w-3.5 h-3.5 text-[#16A34A]" />
        ) : isFail ? (
          <ShieldAlert className="w-3.5 h-3.5 text-[#D97706]" />
        ) : (
          <Shield className="w-3.5 h-3.5 text-[#6B7280]" />
        )}
        <span>
          {isPass
            ? t("webmail_next.security_pass")
            : t("webmail_next.security_warning")}
        </span>
      </button>

      {showDetails && (
        <div
          data-testid="security-details-popover"
          className="absolute left-0 top-7 z-50 w-64 p-3 bg-white rounded-lg shadow-lg border border-[#EBE8E0] text-xs space-y-1.5"
        >
          <div className="font-semibold text-[#18181B] pb-1 border-b border-[#F3F1EC]">
            Authentication Details
          </div>
          <div className="flex justify-between text-[#71717A]">
            <span>Domain:</span>
            <span className="font-mono text-[#18181B]">
              {authentication.domain || "N/A"}
            </span>
          </div>
          <div className="flex justify-between text-[#71717A]">
            <span>Status:</span>
            <span
              className={`font-semibold ${isPass ? "text-[#16A34A]" : "text-[#D97706]"}`}
            >
              {authentication.status.toUpperCase()}
            </span>
          </div>
          {authentication.reason && (
            <div className="text-[11px] text-[#A1A1AA] pt-1">
              {authentication.reason}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
