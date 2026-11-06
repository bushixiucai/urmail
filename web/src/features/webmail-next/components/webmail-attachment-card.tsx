import {
  Download,
  FileText,
  Image as ImageIcon,
  FileArchive,
  File,
} from "lucide-react";
import type { AttachmentMeta } from "@/features/webmail/mailbox-api";

interface WebmailAttachmentCardProps {
  attachment: AttachmentMeta;
  folderEnc: string;
  uid: number;
}

export function WebmailAttachmentCard({
  attachment,
  folderEnc,
  uid,
}: WebmailAttachmentCardProps) {
  const downloadUrl = `/api/v1/mailbox/folders/${encodeURIComponent(
    folderEnc,
  )}/messages/${uid}/attachments/${encodeURIComponent(attachment.part_id)}`;

  const getIcon = () => {
    const type = attachment.content_type.toLowerCase();
    if (type.startsWith("image/")) return ImageIcon;
    if (type.includes("pdf") || type.includes("text")) return FileText;
    if (
      type.includes("zip") ||
      type.includes("tar") ||
      type.includes("archive")
    )
      return FileArchive;
    return File;
  };

  const Icon = getIcon();

  const formatSize = (bytes: number) => {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  };

  return (
    <div
      data-testid={`attachment-card-${attachment.part_id}`}
      className="flex items-center justify-between p-2.5 bg-white border border-[#EBE8E0] rounded-lg shadow-sm hover:border-[#D4D4D8] transition-all group"
    >
      <div className="flex items-center gap-2.5 min-w-0 pr-2">
        <div className="w-8 h-8 rounded bg-[#F6F4EE] flex items-center justify-center shrink-0 text-[#71717A]">
          <Icon className="w-4 h-4" />
        </div>
        <div className="min-w-0">
          <div
            className="text-xs font-medium text-[#18181B] truncate"
            title={attachment.filename}
          >
            {attachment.filename}
          </div>
          <div className="text-[10px] text-[#52525B]">
            {formatSize(attachment.size)}
          </div>
        </div>
      </div>

      <a
        data-testid={`download-attachment-${attachment.part_id}`}
        href={downloadUrl}
        download={attachment.filename}
        className="p-1.5 text-[#71717A] hover:text-[#18181B] rounded hover:bg-[#F3F1EC] transition-colors"
        title="Download Attachment"
      >
        <Download className="w-4 h-4" />
      </a>
    </div>
  );
}
