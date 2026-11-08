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
  riskLevel?: "low" | "medium" | "high" | "critical";
  indicators?: Array<{ type: string; evidence?: string }>;
}

export function WebmailSecurityBadge({
  authentication,
  riskLevel,
  indicators,
}: WebmailSecurityBadgeProps) {
  const { t } = useTranslation();
  const [showDetails, setShowDetails] = useState(false);

  if (!authentication && !riskLevel) return null;

  const isHighRisk =
    riskLevel === "high" ||
    riskLevel === "critical" ||
    authentication?.status === "fail";
  const isPass =
    (authentication?.status === "pass" || !authentication) &&
    (riskLevel === "low" || !riskLevel);

  return (
    <div className="relative inline-block">
      <button
        data-testid="security-badge-button"
        type="button"
        onClick={() => setShowDetails((p) => !p)}
        className={`flex items-center gap-1 px-1.5 py-0.5 rounded text-xs font-medium transition-colors ${
          isHighRisk
            ? "bg-[#FEE2E2] text-[#991B1B] hover:bg-[#FECACA]"
            : isPass
              ? "bg-[#DCFCE7] text-[#166534] hover:bg-[#BBF7D0]"
              : "bg-[#FEF3C7] text-[#92400E] hover:bg-[#FDE68A]"
        }`}
        title="Security & Authentication Details"
      >
        {isHighRisk ? (
          <ShieldAlert className="w-3.5 h-3.5 text-[#DC2626]" />
        ) : isPass ? (
          <ShieldCheck className="w-3.5 h-3.5 text-[#16A34A]" />
        ) : (
          <Shield className="w-3.5 h-3.5 text-[#D97706]" />
        )}
        <span>
          {isHighRisk
            ? t("webmail_next.security_warning")
            : isPass
              ? t("webmail_next.security_pass")
              : t("webmail_next.security_warning")}
        </span>
      </button>

      {showDetails && (
        <div
          data-testid="security-details-popover"
          className="absolute left-0 top-7 z-50 w-72 p-3 bg-white rounded-lg shadow-lg border border-[#EBE8E0] text-xs space-y-2"
        >
          <div className="font-semibold text-[#18181B] pb-1 border-b border-[#F3F1EC]">
            Security Details
          </div>
          {authentication && (
            <>
              <div className="flex justify-between text-[var(--vellum-ink-secondary)]">
                <span>Domain:</span>
                <span className="font-mono text-[#18181B]">
                  {authentication.domain || "N/A"}
                </span>
              </div>
              <div className="flex justify-between text-[var(--vellum-ink-secondary)]">
                <span>Status:</span>
                <span
                  className={`font-semibold ${
                    authentication.status === "pass"
                      ? "text-[#16A34A]"
                      : "text-[#DC2626]"
                  }`}
                >
                  {authentication.status.toUpperCase()}
                </span>
              </div>
              {authentication.reason && (
                <div className="text-xs text-[#52525B]">
                  {authentication.reason}
                </div>
              )}
            </>
          )}
          {riskLevel && (
            <div className="flex justify-between text-[var(--vellum-ink-secondary)] pt-1 border-t border-[#F3F1EC]">
              <span>Risk Level:</span>
              <span
                className={`font-semibold ${
                  riskLevel === "low"
                    ? "text-[#16A34A]"
                    : riskLevel === "medium"
                      ? "text-[#D97706]"
                      : "text-[#DC2626]"
                }`}
              >
                {riskLevel.toUpperCase()}
              </span>
            </div>
          )}
          {indicators && indicators.length > 0 && (
            <div className="pt-1 border-t border-[#F3F1EC]">
              <div className="text-[var(--vellum-ink-secondary)] font-medium mb-1">
                Indicators:
              </div>
              <ul className="list-disc list-inside space-y-0.5 text-[#991B1B]">
                {indicators.map((ind, i) => (
                  <li key={i} className="break-words">
                    <span className="font-medium">{ind.type}</span>
                    {ind.evidence && `: ${ind.evidence}`}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
