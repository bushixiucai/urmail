import { useEditor, EditorContent } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Link from "@tiptap/extension-link";
import {
  Bold,
  Italic,
  Strikethrough,
  List,
  ListOrdered,
  Quote,
  Code,
  FileCode,
} from "lucide-react";

interface WebmailEditorTiptapProps {
  content: string;
  isPlainText: boolean;
  onChange: (html: string, text: string) => void;
  onTogglePlainText: () => void;
}

export function WebmailEditorTiptap({
  content,
  isPlainText,
  onChange,
  onTogglePlainText,
}: WebmailEditorTiptapProps) {
  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: { levels: [1, 2, 3] },
      }),
      Link.configure({
        openOnClick: false,
      }),
    ],
    content,
    onUpdate: ({ editor }) => {
      onChange(editor.getHTML(), editor.getText());
    },
    editorProps: {
      attributes: {
        class:
          "prose prose-sm max-w-none focus:outline-none min-h-[220px] p-3 text-sm text-[#18181B]",
      },
    },
  });

  if (isPlainText) {
    return (
      <div className="flex-1 flex flex-col min-h-[260px] bg-white">
        <textarea
          data-testid="plaintext-editor"
          value={content}
          onChange={(e) => onChange(e.target.value, e.target.value)}
          placeholder="Compose email in plain text..."
          className="flex-1 w-full p-3 text-sm text-[#18181B] outline-none resize-none font-mono"
        />
        <div className="h-9 px-3 border-t border-[#EBE8E0] bg-[#FAF9F5] flex items-center justify-between text-xs text-[#71717A]">
          <span className="text-[11px]">Plain Text Mode</span>
          <button
            type="button"
            onClick={onTogglePlainText}
            className="text-[11px] text-[var(--brand-primary,#2563eb)] hover:underline"
          >
            Switch to Rich Text
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 flex flex-col min-h-[260px] bg-white overflow-hidden">
      <div className="flex-1 overflow-y-auto">
        <EditorContent editor={editor} data-testid="tiptap-editor" />
      </div>

      {/* Modern Bottom Toolbar */}
      <div className="h-9 px-2 border-t border-[#EBE8E0] bg-[#FAF9F5] flex items-center justify-between text-[#71717A] text-xs shrink-0 select-none">
        <div className="flex items-center gap-0.5">
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleBold().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("bold") ? "bg-[#EBE8E0] text-[#18181B]" : ""
            }`}
            title="Bold (Ctrl+B)"
          >
            <Bold className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleItalic().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("italic") ? "bg-[#EBE8E0] text-[#18181B]" : ""
            }`}
            title="Italic (Ctrl+I)"
          >
            <Italic className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleStrike().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("strike") ? "bg-[#EBE8E0] text-[#18181B]" : ""
            }`}
            title="Strikethrough"
          >
            <Strikethrough className="w-3.5 h-3.5" />
          </button>

          <div className="w-[1px] h-4 bg-[#EBE8E0] mx-1" />

          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleBulletList().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("bulletList")
                ? "bg-[#EBE8E0] text-[#18181B]"
                : ""
            }`}
            title="Bullet List"
          >
            <List className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleOrderedList().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("orderedList")
                ? "bg-[#EBE8E0] text-[#18181B]"
                : ""
            }`}
            title="Numbered List"
          >
            <ListOrdered className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleBlockquote().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("blockquote")
                ? "bg-[#EBE8E0] text-[#18181B]"
                : ""
            }`}
            title="Quote"
          >
            <Quote className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleCodeBlock().run()}
            className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
              editor?.isActive("codeBlock") ? "bg-[#EBE8E0] text-[#18181B]" : ""
            }`}
            title="Code Block"
          >
            <Code className="w-3.5 h-3.5" />
          </button>
        </div>

        <div>
          <button
            type="button"
            onClick={onTogglePlainText}
            className="flex items-center gap-1 text-[11px] hover:text-[#18181B] px-1.5 py-0.5 rounded hover:bg-[#EBE8E0]"
            title="Switch to Plain Text Mode"
          >
            <FileCode className="w-3.5 h-3.5" />
            <span>Plain Text</span>
          </button>
        </div>
      </div>
    </div>
  );
}
