import { useState, useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
  listContacts,
  createContact,
  type MailboxContact,
} from "@/features/webmail/mailbox-api";
import { X, UserPlus, Mail, Search, AlertCircle, BookUser } from "lucide-react";

interface WebmailContactsModalProps {
  isOpen: boolean;
  onClose: () => void;
  onComposeTo?: (email: string) => void;
}

const ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ#".split("");

export function WebmailContactsModal({
  isOpen,
  onClose,
  onComposeTo,
}: WebmailContactsModalProps) {
  const { t } = useTranslation();
  const [contacts, setContacts] = useState<MailboxContact[]>([]);
  const [loading, setLoading] = useState(true);
  const [searchQuery, setSearchQuery] = useState("");
  const [isAdding, setIsAdding] = useState(false);
  const [newName, setNewName] = useState("");
  const [newEmail, setNewEmail] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isOpen) return;
    const fetchContacts = async () => {
      try {
        setLoading(true);
        setError(null);
        const res = await listContacts();
        setContacts(res.contacts || []);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        setError(msg || "Failed to load contacts.");
      } finally {
        setLoading(false);
      }
    };
    fetchContacts();
  }, [isOpen]);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && isOpen) {
        onClose();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen, onClose]);

  const handleCreateContact = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newName.trim() || !newEmail.trim()) return;

    try {
      setSaving(true);
      setError(null);
      const newContact = await createContact(newName.trim(), newEmail.trim());
      setContacts((prev) => [...prev, newContact]);
      setNewName("");
      setNewEmail("");
      setIsAdding(false);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(msg || t("contacts.err_create", "Failed to add contact."));
    } finally {
      setSaving(false);
    }
  };

  // Group contacts by initial
  const filteredContacts = useMemo(() => {
    const q = searchQuery.toLowerCase().trim();
    if (!contacts || !Array.isArray(contacts)) return [];
    if (!q) return contacts;
    return contacts.filter((c) => {
      if (!c) return false;
      const name = (c.display_name || "").toLowerCase();
      const hasEmailMatch =
        Array.isArray(c.emails) &&
        c.emails.some((e) => (e?.address || "").toLowerCase().includes(q));
      return name.includes(q) || hasEmailMatch;
    });
  }, [contacts, searchQuery]);

  const groupedContacts = useMemo(() => {
    const map: Record<string, MailboxContact[]> = {};
    for (const contact of filteredContacts) {
      if (!contact) continue;
      const name =
        contact.display_name?.trim() ||
        contact.emails?.[0]?.address?.trim() ||
        t("contacts.unnamed", "Unnamed Contact");
      const first = (name.charAt(0) || "#").toUpperCase();
      const key = /[A-Z]/.test(first) ? first : "#";
      if (!map[key]) map[key] = [];
      map[key].push(contact);
    }
    // Sort contacts within group
    for (const key of Object.keys(map)) {
      map[key].sort((a, b) => {
        const nameA =
          a?.display_name?.trim() || a?.emails?.[0]?.address?.trim() || "";
        const nameB =
          b?.display_name?.trim() || b?.emails?.[0]?.address?.trim() || "";
        return nameA.localeCompare(nameB);
      });
    }
    return map;
  }, [filteredContacts, t]);

  if (!isOpen) return null;

  return (
    <div
      data-testid="webmail-contacts-modal"
      className="fixed inset-0 z-[500] flex justify-end bg-black/40 backdrop-blur-xs select-none transition-all duration-300"
    >
      <div className="w-full sm:max-w-xl h-full bg-[var(--vellum-surface)] shadow-2xl flex flex-col border-l border-[var(--vellum-border)] overflow-hidden">
        {/* Header */}
        <div className="h-14 px-6 border-b border-[var(--vellum-border)] bg-[var(--vellum-ground)] flex items-center justify-between shrink-0">
          <div className="flex items-center gap-2 font-bold text-base text-[var(--vellum-ink)]">
            <BookUser className="w-5 h-5 text-[var(--brand-primary,var(--brand-primary))]" />
            <span>{t("contacts.title", "Personal Contacts")}</span>
          </div>
          <button
            data-testid="btn-close-contacts"
            type="button"
            onClick={onClose}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] transition-colors"
            title="Close"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Toolbar: Search & Add button */}
        <div className="p-4 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] space-y-3 shrink-0">
          <div className="flex items-center gap-2">
            <div className="relative flex-1">
              <Search className="w-4 h-4 text-[var(--vellum-ink-secondary)] absolute left-3 top-2.5 pointer-events-none" />
              <input
                data-testid="contacts-search-input"
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder={t(
                  "contacts.search_placeholder",
                  "Search contacts by name or email...",
                )}
                className="w-full h-8 pl-9 pr-3 text-xs rounded-full border border-[var(--vellum-ink-disabled)] bg-[var(--vellum-surface-muted)] focus:bg-white focus:border-[var(--brand-primary,var(--brand-primary))] focus:outline-none"
              />
            </div>
            <button
              data-testid="btn-toggle-add-contact"
              type="button"
              onClick={() => setIsAdding((prev) => !prev)}
              className="px-3 py-1.5 bg-[var(--brand-primary,var(--brand-primary))] text-white text-xs font-semibold rounded-md shadow-xs hover:brightness-105 flex items-center gap-1.5"
            >
              <UserPlus className="w-3.5 h-3.5" />
              <span>
                {isAdding
                  ? t("common.cancel", "Cancel")
                  : t("contacts.add_btn", "New Contact")}
              </span>
            </button>
          </div>

          {/* New contact inline form */}
          {isAdding && (
            <form
              onSubmit={handleCreateContact}
              data-testid="add-contact-form"
              className="p-3 bg-[var(--vellum-ground)] border border-[var(--vellum-border)] rounded-lg space-y-3"
            >
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                <input
                  data-testid="input-contact-name"
                  type="text"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder={t("contacts.name_placeholder", "Full Name")}
                  required
                  className="h-8 px-2 text-xs rounded border border-[var(--vellum-ink-disabled)] bg-white focus:outline-none focus:border-[var(--brand-primary,var(--brand-primary))]"
                />
                <input
                  data-testid="input-contact-email"
                  type="email"
                  value={newEmail}
                  onChange={(e) => setNewEmail(e.target.value)}
                  placeholder={t(
                    "contacts.email_placeholder",
                    "email@example.com",
                  )}
                  required
                  className="h-8 px-2 text-xs rounded border border-[var(--vellum-ink-disabled)] bg-white focus:outline-none focus:border-[var(--brand-primary,var(--brand-primary))]"
                />
              </div>
              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setIsAdding(false)}
                  className="px-3 py-1 text-xs text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)]"
                >
                  {t("common.cancel", "Cancel")}
                </button>
                <button
                  data-testid="btn-save-new-contact"
                  type="submit"
                  disabled={saving}
                  className="px-3 py-1 bg-[var(--brand-primary,var(--brand-primary))] text-white text-xs font-semibold rounded shadow-xs hover:brightness-105"
                >
                  {saving
                    ? t("common.saving", "Saving...")
                    : t("contacts.save_btn", "Save")}
                </button>
              </div>
            </form>
          )}

          {error && (
            <div className="p-2 bg-[#FEF2F2] border border-[#FECACA] rounded text-[#991B1B] text-xs flex items-center gap-1.5">
              <AlertCircle className="w-3.5 h-3.5 shrink-0" />
              <span>{error}</span>
            </div>
          )}
        </div>

        {/* Contact List & Alphabet Sidebar */}
        <div className="flex-1 flex overflow-hidden relative">
          <div className="flex-1 overflow-y-auto p-4 space-y-4">
            {loading ? (
              <div className="p-8 text-center text-xs text-[var(--vellum-ink-secondary)]">
                {t("common.loading", "Loading contacts...")}
              </div>
            ) : Object.keys(groupedContacts).length === 0 ? (
              <div className="p-8 text-center text-xs text-[var(--vellum-ink-secondary)]">
                {t("contacts.no_contacts", "No contacts found.")}
              </div>
            ) : (
              Object.keys(groupedContacts)
                .sort()
                .map((letter) => (
                  <div
                    key={letter}
                    id={`contact-group-${letter}`}
                    className="space-y-1.5"
                  >
                    <div className="sticky top-0 bg-[var(--vellum-ground)] px-2 py-0.5 rounded text-xs font-bold text-[var(--brand-primary,var(--brand-primary))] border border-[var(--vellum-border)]">
                      {letter}
                    </div>
                    <div className="space-y-1">
                      {groupedContacts[letter].map((c) => {
                        const email = c?.emails?.[0]?.address || "";
                        const displayName =
                          c?.display_name?.trim() ||
                          email ||
                          t("contacts.unnamed", "Unnamed Contact");
                        const initial = (
                          displayName.charAt(0) || "?"
                        ).toUpperCase();
                        return (
                          <div
                            key={c.id}
                            data-testid={`contact-card-${c.id}`}
                            className="p-2 bg-[var(--vellum-surface)] border border-[var(--vellum-border)] rounded-lg hover:border-[var(--vellum-ink-disabled)] transition-colors flex items-center justify-between gap-3"
                          >
                            <div className="flex items-center gap-3 min-w-0">
                              <div className="w-8 h-8 rounded-full bg-[#E0E7FF] text-[#3730A3] font-bold text-xs flex items-center justify-center shrink-0">
                                {initial}
                              </div>
                              <div className="min-w-0">
                                <div className="font-semibold text-xs text-[var(--vellum-ink)] truncate">
                                  {displayName}
                                </div>
                                <div className="text-xs text-[var(--vellum-ink-secondary)] truncate font-mono">
                                  {email}
                                </div>
                              </div>
                            </div>

                            {onComposeTo && email && (
                              <button
                                data-testid={`btn-compose-to-${c.id}`}
                                type="button"
                                onClick={() => {
                                  onComposeTo(email);
                                  onClose();
                                }}
                                className="px-2 py-1 text-xs text-[var(--brand-primary,var(--brand-primary))] hover:bg-[var(--vellum-brand-soft)] rounded flex items-center gap-1 border border-[#BFDBFE] transition-colors shrink-0"
                                title="Compose email to contact"
                              >
                                <Mail className="w-3 h-3" />
                                <span>
                                  {t("contacts.write_btn", "Compose")}
                                </span>
                              </button>
                            )}
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ))
            )}
          </div>

          {/* Alphabet Index Quick Jump Rail (A~Z) */}
          <div className="w-7 bg-[var(--vellum-ground)] border-l border-[var(--vellum-border)] py-2 flex flex-col items-center justify-between text-xs text-[var(--vellum-ink-secondary)] font-semibold select-none shrink-0 overflow-y-auto">
            {ALPHABET.map((letter) => (
              <a
                key={letter}
                href={`#contact-group-${letter}`}
                className="hover:text-[var(--brand-primary,var(--brand-primary))] px-1 py-0.5"
              >
                {letter}
              </a>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
