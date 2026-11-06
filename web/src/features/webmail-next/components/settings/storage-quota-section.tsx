import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { getMailboxQuota } from "@/features/webmail/mailbox-api";
import {
  HardDrive,
  AlertTriangle,
  AlertOctagon,
  CheckCircle2,
  Paperclip,
  RefreshCw,
} from "lucide-react";

function formatBytes(bytes: number): string {
  if (bytes < 0) return "Unlimited";
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
}

export function StorageQuotaSection() {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [quota, setQuota] = useState<{
    used_bytes: number;
    quota_bytes: number;
    max_message_bytes?: number;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const fetchQuota = async () => {
    try {
      setLoading(true);
      setError(null);
      const data = await getMailboxQuota();
      setQuota(data);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.quota.err_fetch", "Failed to load quota info."),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchQuota();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const used = quota?.used_bytes || 0;
  const total = quota?.quota_bytes || 1;
  const isUnlimited = total <= 0;
  const percent = isUnlimited
    ? 0
    : Math.min(100, Math.round((used / total) * 100));

  const isAlert = percent >= 95;
  const isWarning = percent >= 80 && percent < 95;

  return (
    <div className="space-y-6" data-testid="storage-quota-section">
      <div className="border-b border-[#EBE8E0] pb-4 flex items-center justify-between">
        <div>
          <h3 className="text-base font-semibold text-[#18181B] flex items-center gap-2">
            <HardDrive className="w-5 h-5 text-[var(--brand-primary,#2563eb)]" />
            <span>{t("settings.quota.title", "Storage & Quota")}</span>
          </h3>
          <p className="text-xs text-[#52525B] mt-1">
            {t(
              "settings.quota.subtitle",
              "Monitor your mailbox capacity, message storage, and attachment limits.",
            )}
          </p>
        </div>
        <button
          type="button"
          onClick={fetchQuota}
          disabled={loading}
          className="p-1.5 rounded text-[#71717A] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
          title={t("common.refresh", "Refresh")}
        >
          <RefreshCw className={`w-4 h-4 ${loading ? "animate-spin" : ""}`} />
        </button>
      </div>

      {error ? (
        <div className="p-3 bg-[#FEF2F2] border border-[#FECACA] rounded-lg text-[#991B1B] text-xs">
          {error}
        </div>
      ) : loading ? (
        <div className="p-6 text-center text-xs text-[#71717A]">
          {t("common.loading", "Loading storage statistics...")}
        </div>
      ) : (
        <div className="space-y-6 max-w-xl">
          {/* Main Progress Bar Card */}
          <div className="p-5 bg-[#FFFFFF] border border-[#EBE8E0] rounded-xl space-y-4 shadow-sm">
            <div className="flex justify-between items-baseline">
              <span className="text-xs font-semibold text-[#18181B]">
                {t("settings.quota.used_label", "Used Storage")}
              </span>
              <div className="text-right">
                <span className="text-lg font-bold text-[#18181B]">
                  {percent}%
                </span>
                <span className="text-xs text-[#71717A] ml-2">
                  ({formatBytes(used)} /{" "}
                  {isUnlimited
                    ? t("settings.quota.unlimited", "Unlimited")
                    : formatBytes(total)}
                  )
                </span>
              </div>
            </div>

            {/* Dual layer progress bar */}
            <div className="w-full h-3 bg-[#EBE8E0] rounded-full overflow-hidden p-0.5">
              <div
                data-testid="quota-progress-bar"
                className={`h-full rounded-full transition-all duration-500 ${
                  isAlert
                    ? "bg-[#DC2626]"
                    : isWarning
                      ? "bg-[#D97706]"
                      : "bg-[var(--brand-primary,#2563eb)]"
                }`}
                style={{ width: `${percent}%` }}
              />
            </div>

            {/* Warning or Status Callout */}
            {isAlert ? (
              <div
                data-testid="quota-alert-callout"
                className="p-3 bg-[#FEF2F2] border border-[#FCA5A5] rounded-lg flex items-start gap-2.5 text-[#991B1B] text-xs"
              >
                <AlertOctagon className="w-4 h-4 shrink-0 mt-0.5 text-[#DC2626]" />
                <div>
                  <span className="font-semibold">
                    {t(
                      "settings.quota.alert_heading",
                      "Critical Space Shortage",
                    )}
                  </span>
                  <p className="mt-0.5 text-[11px] text-[#B91C1C]">
                    {t(
                      "settings.quota.alert_desc",
                      "Mailbox storage is critically low, which may lead to inbound message rejection. Please delete unnecessary messages or large attachments.",
                    )}
                  </p>
                </div>
              </div>
            ) : isWarning ? (
              <div
                data-testid="quota-warning-callout"
                className="p-3 bg-[#FFFBEB] border border-[#FDE68A] rounded-lg flex items-start gap-2.5 text-[#92400E] text-xs"
              >
                <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5 text-[#D97706]" />
                <div>
                  <span className="font-semibold">
                    {t(
                      "settings.quota.warning_heading",
                      "Approaching Storage Limit",
                    )}
                  </span>
                  <p className="mt-0.5 text-[11px] text-[#B45309]">
                    {t(
                      "settings.quota.warning_desc",
                      "Your mailbox is running out of space. Consider archiving or deleting older messages with large attachments.",
                    )}
                  </p>
                </div>
              </div>
            ) : (
              <div className="flex items-center gap-2 text-xs text-[#15803D]">
                <CheckCircle2 className="w-4 h-4 text-[#16A34A]" />
                <span>
                  {t("settings.quota.healthy", "Storage space is healthy.")}
                </span>
              </div>
            )}
          </div>

          {/* Attachment and Send Limits Note */}
          <div className="p-4 bg-[#FAF9F5] border border-[#EBE8E0] rounded-xl space-y-2">
            <h4 className="text-xs font-semibold text-[#18181B] flex items-center gap-1.5">
              <Paperclip className="w-3.5 h-3.5 text-[#71717A]" />
              <span>
                {t(
                  "settings.quota.attachment_limits_title",
                  "Mailbox Delivery Specifications",
                )}
              </span>
            </h4>
            <ul className="text-xs text-[#52525B] space-y-1 list-disc list-inside">
              <li>
                {t(
                  "settings.quota.limit_total_attachment",
                  "Total attachments per message limit: 10 MB (MAX_SEND_TOTAL_ATTACHMENT_BYTES)",
                )}
              </li>
              <li>
                {t(
                  "settings.quota.limit_single_attachment",
                  "Single attachment file limit: 5 MB",
                )}
              </li>
              <li>
                {t(
                  "settings.quota.limit_auto_compress",
                  "Lossless compression prompt will be provided when uploading large images to ensure deliverability",
                )}
              </li>
            </ul>
          </div>
        </div>
      )}
    </div>
  );
}
