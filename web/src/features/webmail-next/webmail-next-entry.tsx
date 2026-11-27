import { useParams, useSearchParams } from "react-router-dom";
import { WebmailNextWorkbench } from "./webmail-next-workbench";
import { WebmailErrorBoundary } from "./components/webmail-error-boundary";

export function WebmailNextEntry() {
  const params = useParams<{ folderEnc?: string }>();
  const [searchParams] = useSearchParams();

  const folder = params.folderEnc || searchParams.get("folder") || "INBOX";

  return (
    <WebmailErrorBoundary>
      <WebmailNextWorkbench initialFolder={folder} />
    </WebmailErrorBoundary>
  );
}
