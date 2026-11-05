import React from "react";
import { useParams, useSearchParams } from "react-router-dom";
import { WebmailNextWorkbench } from "./webmail-next-workbench";

export function WebmailNextEntry() {
  const params = useParams<{ folderEnc?: string }>();
  const [searchParams] = useSearchParams();

  const folder = params.folderEnc || searchParams.get("folder") || "INBOX";

  return <WebmailNextWorkbench initialFolder={folder} />;
}
