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

/** Thin folder search input; submits q to the parent (IMAP SEARCH via listMessages). */
export function MailboxSearchBox({
  value,
  disabled,
  onSearch,
}: MailboxSearchBoxProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(value);

  useEffect(() => {
    setDraft(value);
  }, [value]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSearch(draft.trim());
  };

  return (
    <form
      className="flex gap-1.5 items-center px-3 py-2 border-b"
      onSubmit={submit}
      role="search"
    >
      <Input
        value={draft}
        disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
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
    </form>
  );
}
