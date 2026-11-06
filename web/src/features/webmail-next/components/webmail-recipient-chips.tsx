import React, { useState, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { X, User } from "lucide-react";
import {
  searchCorporateContacts,
  type CorporateContact,
} from "@/features/webmail/mailbox-api";

interface WebmailRecipientChipsProps {
  label: string;
  recipients: string[];
  onChange: (recipients: string[]) => void;
  placeholder?: string;
  testId?: string;
}

export function WebmailRecipientChips({
  label,
  recipients,
  onChange,
  placeholder,
  testId = "recipient-input",
}: WebmailRecipientChipsProps) {
  const { t } = useTranslation();
  const [inputValue, setInputValue] = useState("");
  const [suggestions, setSuggestions] = useState<CorporateContact[]>([]);
  const [selectedIndex, setSelectedIndex] = useState<number>(-1);
  const [isDropdownOpen, setIsDropdownOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Debounced search for corporate directory autocomplete
  useEffect(() => {
    const trimmed = inputValue.trim();
    if (trimmed.length < 1) {
      setSuggestions([]);
      setIsDropdownOpen(false);
      return;
    }

    const timer = setTimeout(async () => {
      try {
        const res = await searchCorporateContacts(trimmed);
        const list = res.contacts || [];
        setSuggestions(list);
        setIsDropdownOpen(list.length > 0);
        setSelectedIndex(-1);
      } catch {
        setSuggestions([]);
        setIsDropdownOpen(false);
      }
    }, 200);

    return () => clearTimeout(timer);
  }, [inputValue]);

  // Click outside to close dropdown
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setIsDropdownOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const addRecipient = (email: string) => {
    const cleanEmail = email.trim();
    if (cleanEmail && !recipients.includes(cleanEmail)) {
      onChange([...recipients, cleanEmail]);
    }
    setInputValue("");
    setSuggestions([]);
    setIsDropdownOpen(false);
    setSelectedIndex(-1);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (isDropdownOpen && suggestions.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSelectedIndex((prev) =>
          prev < suggestions.length - 1 ? prev + 1 : 0,
        );
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setSelectedIndex((prev) =>
          prev > 0 ? prev - 1 : suggestions.length - 1,
        );
        return;
      }
      if (
        e.key === "Enter" &&
        selectedIndex >= 0 &&
        selectedIndex < suggestions.length
      ) {
        e.preventDefault();
        const contact = suggestions[selectedIndex];
        const email = contact?.emails?.[0]?.address;
        if (email) {
          addRecipient(email);
          return;
        }
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setIsDropdownOpen(false);
        return;
      }
    }

    if (e.key === "Enter" || e.key === "," || e.key === " ") {
      e.preventDefault();
      const val = inputValue.trim().replace(/,/g, "");
      if (val) {
        addRecipient(val);
      }
    } else if (e.key === "Backspace" && !inputValue && recipients.length > 0) {
      onChange(recipients.slice(0, -1));
    }
  };

  const handleRemove = (email: string) => {
    onChange(recipients.filter((r) => r !== email));
  };

  return (
    <div
      ref={containerRef}
      className="relative flex flex-wrap items-center gap-1.5 py-1 text-xs border-b border-[#F3F1EC]"
    >
      <span className="w-12 text-[#71717A] shrink-0 font-medium">{label}:</span>
      <div className="flex-1 flex flex-wrap items-center gap-1 relative">
        {recipients.map((email) => (
          <div
            key={email}
            data-testid={`recipient-pill-${email}`}
            className="flex items-center gap-1 pl-2 pr-1 py-0.5 bg-[#EFF6FF] text-[#1E40AF] rounded-full text-xs font-medium border border-[#DBEAFE]"
          >
            <span>{email}</span>
            <button
              type="button"
              onClick={() => handleRemove(email)}
              className="p-0.5 rounded-full hover:bg-[#BFDBFE] text-[#1E40AF]"
            >
              <X className="w-3 h-3" />
            </button>
          </div>
        ))}
        <input
          data-testid={testId}
          type="text"
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          onKeyDown={handleKeyDown}
          onFocus={() => {
            if (suggestions.length > 0) setIsDropdownOpen(true);
          }}
          placeholder={
            recipients.length === 0
              ? (placeholder ?? t("compose.recipient_placeholder"))
              : ""
          }
          className="flex-1 min-w-[120px] bg-transparent outline-none text-xs text-[#18181B] py-0.5"
        />

        {/* Corporate Directory Autocomplete Dropdown Card */}
        {isDropdownOpen && suggestions.length > 0 && (
          <div
            data-testid="corporate-autocomplete-dropdown"
            className="absolute left-0 top-full mt-1 w-80 max-h-56 overflow-y-auto bg-[#FFFFFF] border border-[#EBE8E0] rounded-lg shadow-xl z-50 py-1"
          >
            {suggestions.map((contact, idx) => {
              const primaryEmail = contact?.emails?.[0]?.address || "";
              const deptOrTitle =
                contact?.department ||
                contact?.job_title ||
                contact?.organization ||
                "";
              const isSelected = idx === selectedIndex;

              return (
                <div
                  key={contact?.id || idx}
                  data-testid={`corporate-suggestion-${idx}`}
                  onClick={() => addRecipient(primaryEmail)}
                  onMouseEnter={() => setSelectedIndex(idx)}
                  className={`px-3 py-2 cursor-pointer flex items-center gap-2.5 transition-colors ${
                    isSelected
                      ? "bg-[#EFF6FF] text-[#1E40AF]"
                      : "hover:bg-[#FAF9F5] text-[#18181B]"
                  }`}
                >
                  {/* Avatar badge */}
                  <div className="w-7 h-7 rounded-full bg-[#E0E7FF] text-[#3730A3] font-bold text-xs flex items-center justify-center shrink-0">
                    {contact?.display_name?.trim() ? (
                      contact.display_name.trim().charAt(0).toUpperCase()
                    ) : (
                      <User className="w-3.5 h-3.5" />
                    )}
                  </div>

                  {/* Name, department, email */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="font-semibold text-xs truncate">
                        {contact?.display_name?.trim() ||
                          primaryEmail ||
                          "Unknown"}
                      </span>
                      {deptOrTitle && (
                        <span className="text-[10px] text-[#71717A] truncate">
                          · {deptOrTitle}
                        </span>
                      )}
                    </div>
                    <div className="text-[11px] text-[#52525B] truncate font-mono">
                      {primaryEmail}
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
