import { CrepeBuilder } from "@milkdown/crepe/builder";
import { blockEdit } from "@milkdown/crepe/feature/block-edit";
import { cursor } from "@milkdown/crepe/feature/cursor";
import { linkTooltip } from "@milkdown/crepe/feature/link-tooltip";
import { listItem } from "@milkdown/crepe/feature/list-item";
import { placeholder } from "@milkdown/crepe/feature/placeholder";
import { toolbar } from "@milkdown/crepe/feature/toolbar";
import { Milkdown, MilkdownProvider, useEditor } from "@milkdown/react";
import { useEffect, useRef } from "react";

type MarkdownEditorProps = {
  label: string;
  value: string;
  onChange: (markdown: string) => void;
  size?: "default" | "compact";
};

function Editor({ value, onChange }: Pick<MarkdownEditorProps, "value" | "onChange">) {
  const onChangeRef = useRef(onChange);
  useEffect(() => { onChangeRef.current = onChange; }, [onChange]);

  useEditor((root) => new CrepeBuilder({ root, defaultValue: value })
    .addFeature(blockEdit)
    .addFeature(cursor)
    .addFeature(linkTooltip)
    .addFeature(listItem)
    .addFeature(placeholder, { text: "Write Markdown…" })
    .addFeature(toolbar)
    .on((listener) => {
    listener.markdownUpdated((_ctx, markdown, previous) => {
      if (markdown !== previous) onChangeRef.current(markdown);
    });
  }), []);

  return <Milkdown />;
}

export function MarkdownEditor({ label, value, onChange, size = "default" }: MarkdownEditorProps) {
  return <div className="markdown-editor-field">
    <span className="markdown-editor-label">{label}</span>
    <div className={`markdown-editor markdown-editor--${size}`} role="group" aria-label={label}>
      <MilkdownProvider><Editor value={value} onChange={onChange} /></MilkdownProvider>
    </div>
  </div>;
}
