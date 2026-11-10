// web/src/features/webmail-next/types.ts

export type DensityMode = "comfortable" | "compact" | "list_only";
export type SplitViewMode = "vertical" | "horizontal" | "none";
export type SortField = "date" | "subject" | "from" | "size";
export type SortOrder = "asc" | "desc";

export interface MailboxPreferences {
  webmail_version?: "legacy" | "next";
  density?: DensityMode;
  split_mode?: SplitViewMode;
  read_timer_mode?: "immediate" | "delay_5s" | "manual";
  allow_all_remote_images?: boolean;
  trusted_image_senders?: string[];
  [key: string]: unknown;
}

export interface WebmailItem {
  uid: number;
  uid_validity?: number;
  subject: string;
  sender: string;
  senderEmail: string;
  recipients?: string[];
  date: string;
  snippet?: string;
  isUnread: boolean;
  isStarred: boolean;
  hasAttachments: boolean;
  size: number;
  threadId?: number;
  threadCount?: number;
  childUids?: number[];
  isThreadExpanded?: boolean;
  /** True when this row is an expanded child of a thread (not the representative). */
  isThreadChild?: boolean;
  /** Last child in the expanded list — stops the connector line mid-row. */
  isLastThreadChild?: boolean;
  /** #119 degraded contract: set for rows whose candidate group the
   * server could not verify (thread_id/thread_count omitted for them). */
  threadVerification?: "unverified";
  folder?: string;
  folderEnc?: string;
}

export interface ComposeState {
  isOpen: boolean;
  isMinimized: boolean;
  isMaximized: boolean;
  draftUid?: number;
  draftUidValidity?: number;
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  html: string;
  isPlainText: boolean;
  attachments: ComposeAttachment[];
  lastSavedAt?: string;
  isSaving: boolean;
  isSending: boolean;
}

export interface ComposeAttachment {
  id: string;
  filename: string;
  contentType: string;
  size: number;
  contentBase64: string;
  error?: string;
}
