import { Navigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useMailboxNative } from "./use-mailbox-native";
import { WebmailHelpPage } from "./webmail-help-page";

/**
 * Panel nav lands here at /webmail.
 * native=false → client-setup help; native=true → inbox SPA.
 */
export function WebmailEntryPage() {
  const { t } = useTranslation();
  const { native, loaded } = useMailboxNative();

  if (!loaded) {
    return (
      <p className="text-sm text-muted-foreground">{t("state.loading")}</p>
    );
  }
  if (native) {
    return <Navigate to="/webmail/inbox" replace />;
  }
  return <WebmailHelpPage />;
}
