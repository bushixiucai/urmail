import { FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
export interface MailboxSearchBoxProps {
  value: string;
  disabled?: boolean;
  onSearch: (q: string) => void;
}

export function splitMailboxSearch(value: string) {
  const tokens = value.match(/(?:[^\s"\\]|\\.|"(?:[^"\\]|\\.)*")+/g) ?? [];
  const dates = tokens.map((token) =>
    token.match(/^(since|before):(\d{4}-\d{2}-\d{2})$/),
  );
  const count = (key: string) =>
    dates.filter((item) => item?.[1] === key).length;
  let since = "",
    before = "",
    attachment = false;
  const remaining = tokens.filter((token, index) => {
    const date = dates[index];
    if (date && count(date[1]) === 1) {
      if (date[1] === "since") since = date[2];
      else before = date[2];
      return false;
    }
    if (
      token === "has:attachment" &&
      tokens.filter((t) => t === "has:attachment").length === 1
    ) {
      attachment = true;
      return false;
    }
    return true;
  });
  return { draft: remaining.join(" "), since, before, attachment };
}

/** Typed filters feed the same deterministic, bounded IMAP search query. */
export function MailboxSearchBox({
  value,
  disabled,
  onSearch,
}: MailboxSearchBoxProps) {
  const { t } = useTranslation();
  const initial = splitMailboxSearch(value);
  const [draft, setDraft] = useState(initial.draft);
  const [since, setSince] = useState(initial.since);
  const [before, setBefore] = useState(initial.before);
  const [attachment, setAttachment] = useState(initial.attachment);
  useEffect(() => {
    const next = splitMailboxSearch(value);
    setDraft(next.draft);
    setSince(next.since);
    setBefore(next.before);
    setAttachment(next.attachment);
  }, [value]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSearch(
      [
        draft.trim(),
        since ? `since:${since}` : "",
        before ? `before:${before}` : "",
        attachment ? "has:attachment" : "",
      ]
        .filter(Boolean)
        .join(" "),
    );
  };
  return (
    <form
      className="space-y-2 px-3 py-2 border-b"
      onSubmit={submit}
      role="search"
    >
      <div className="flex gap-1.5 items-center">
        <Input
          value={draft}
          disabled={disabled}
          onChange={(event) => setDraft(event.target.value)}
          placeholder={t("page.webmail.search_placeholder")}
          aria-label={t("page.webmail.search_aria")}
          className="h-8 text-sm"
        />
        <Button
          type="submit"
          size="sm"
          variant="secondary"
          disabled={disabled}
          className="min-h-11 md:min-h-8"
        >
          <Search className="h-3.5 w-3.5 mr-1" />
          {t("page.webmail.search")}
        </Button>
      </div>
      <details className="text-xs group">
        <summary className="cursor-pointer py-0.5 text-muted-foreground hover:text-foreground inline-flex items-center gap-1 select-none font-medium">
          <span>{t("page.webmail.search_advanced")}</span>
        </summary>
        <div className="mt-2 p-2.5 rounded-lg border border-border/60 bg-muted/20 space-y-2.5">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-xs">
            <label className="space-y-1 text-muted-foreground">
              <span className="block font-medium">
                {t("page.webmail.search_since")}
              </span>
              <Input
                type="date"
                value={since}
                disabled={disabled}
                onChange={(event) => setSince(event.target.value)}
                className="h-7 text-xs bg-background"
                aria-label={t("page.webmail.search_since")}
              />
            </label>
            <label className="space-y-1 text-muted-foreground">
              <span className="block font-medium">
                {t("page.webmail.search_before")}
              </span>
              <Input
                type="date"
                value={before}
                disabled={disabled}
                onChange={(event) => setBefore(event.target.value)}
                className="h-7 text-xs bg-background"
                aria-label={t("page.webmail.search_before")}
              />
            </label>
          </div>
          <label className="inline-flex items-center gap-2 text-xs text-foreground cursor-pointer select-none">
            <input
              type="checkbox"
              checked={attachment}
              disabled={disabled}
              onChange={(event) => setAttachment(event.target.checked)}
              className="h-3.5 w-3.5 rounded border-input accent-primary"
              aria-label={t("page.webmail.search_attachment")}
            />
            <span>{t("page.webmail.search_attachment")}</span>
          </label>
          <p className="text-[11px] text-muted-foreground border-t border-border/40 pt-1.5 leading-relaxed">
            {t("page.webmail.search_query_help")}
          </p>
        </div>
      </details>
    </form>
  );
}
