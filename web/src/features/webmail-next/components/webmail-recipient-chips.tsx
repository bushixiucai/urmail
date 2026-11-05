import React, { useState } from "react";
import { X } from "lucide-react";

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
  placeholder = "Add recipient...",
  testId = "recipient-input",
}: WebmailRecipientChipsProps) {
  const [inputValue, setInputValue] = useState("");

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === "," || e.key === " ") {
      e.preventDefault();
      const val = inputValue.trim().replace(/,/g, "");
      if (val && !recipients.includes(val)) {
        onChange([...recipients, val]);
      }
      setInputValue("");
    } else if (e.key === "Backspace" && !inputValue && recipients.length > 0) {
      onChange(recipients.slice(0, -1));
    }
  };

  const handleRemove = (email: string) => {
    onChange(recipients.filter((r) => r !== email));
  };

  return (
    <div className="flex flex-wrap items-center gap-1.5 py-1 text-xs border-b border-[#F3F1EC]">
      <span className="w-12 text-[#71717A] shrink-0 font-medium">{label}:</span>
      <div className="flex-1 flex flex-wrap items-center gap-1">
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
          placeholder={recipients.length === 0 ? placeholder : ""}
          className="flex-1 min-w-[120px] bg-transparent outline-none text-xs text-[#18181B] py-0.5"
        />
      </div>
    </div>
  );
}
