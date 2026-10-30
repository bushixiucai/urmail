import {
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type Ref,
} from "react";
import { useTranslation } from "react-i18next";
import { Reply } from "lucide-react";
import { Button } from "@/components/ui/button";
import type {
  MessageDetail,
  MailboxSettingsView,
  MailboxIdentity,
} from "./mailbox-api";
import {
  draftFromMessage,
  MailboxComposeDialog,
  type ComposeControllerHandle,
} from "./mailbox-compose-dialog";

interface MailboxInlineReplyProps {
  ref?: Ref<ComposeControllerHandle>;
  message: MessageDetail;
  folderEnc: string;
  settings: MailboxSettingsView | null;
  identities: MailboxIdentity[];
  onOpenFullCompose: (mode: "reply" | "reply-all") => void;
  onSent: () => void;
  disabled?: boolean;
}

/** Inline and expanded views are presentations of one mounted draft editor. */
export function MailboxInlineReply({
  ref,
  message,
  folderEnc,
  onSent,
  disabled = false,
}: MailboxInlineReplyProps) {
  const { t } = useTranslation();
  const [source] = useState(message);
  const [expanded, setExpanded] = useState(false);
  const [mode, setMode] = useState<"reply" | "reply-all">("reply");
  const controller = useRef<ComposeControllerHandle>(null);
  const draft = useMemo(
    () => ({
      ...draftFromMessage(mode, source),
      aiSource: {
        folder_enc: folderEnc,
        uid: source.uid,
        uid_validity: source.uid_validity ?? 0,
      },
    }),
    [mode, source, folderEnc],
  );
  useImperativeHandle(ref, () => ({
    flush: async () => !expanded || Boolean(await controller.current?.flush()),
  }));

  if (expanded)
    return (
      <MailboxComposeDialog
        ref={controller}
        inline
        open
        draft={draft}
        onOpenChange={setExpanded}
        onSent={onSent}
      />
    );
  return (
    <div className="border border-border rounded-xl bg-card p-3 space-y-3">
      <div className="flex items-start gap-2">
        <Reply className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-sm text-muted-foreground min-w-0 break-all">
          {t("page.webmail.inline_reply_to", { recipient: draft.to ?? "" })}
        </span>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={disabled}
          onClick={() => {
            setMode("reply");
            setExpanded(true);
          }}
        >
          {t("page.webmail.compose_reply")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={disabled}
          onClick={() => {
            setMode("reply-all");
            setExpanded(true);
          }}
        >
          {t("page.webmail.compose_reply_all")}
        </Button>
      </div>
    </div>
  );
}
