import { useParams, useSearchParams } from "react-router-dom";
import { WebmailNextWorkbench } from "./webmail-next-workbench";
import { WebmailErrorBoundary } from "./components/webmail-error-boundary";
import { WebmailSessionExpiredBanner } from "./components/webmail-session-expired-banner";

export function WebmailNextEntry() {
  const params = useParams<{ folderEnc?: string }>();
  const [searchParams] = useSearchParams();

  const folder = params.folderEnc || searchParams.get("folder") || "INBOX";

  return (
    <WebmailErrorBoundary>
      {/* Session-expiry recovery surface: keeps the loaded UI usable and
          routes re-login back to the current position (REVIEW-UX-001). */}
      <WebmailSessionExpiredBanner />
      <WebmailNextWorkbench initialFolder={folder} />
    </WebmailErrorBoundary>
  );
}
