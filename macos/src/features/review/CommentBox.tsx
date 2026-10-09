// Writing a review comment, and one written: shared by diffs and the
// file editor.
import { MessageSquarePlus, Trash2 } from "lucide-react";
import { useState } from "react";
import { type Comment, useComments } from "@/state/comments";
import { Button } from "@/ui/Button";

export function CommentEditor({
  onSave,
  onCancel,
  title,
}: {
  onSave: (text: string) => void;
  onCancel: () => void;
  /** What it is about ("Lines 12–14"). */
  title?: string;
}) {
  const [text, setText] = useState("");
  return (
    <div className="m-2 rounded-card border border-separator bg-control p-2 font-sans shadow-sm">
      {title && <div className="mb-1 text-xs text-secondary">{title}</div>}
      <textarea
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") onCancel();
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && text.trim()) onSave(text);
        }}
        placeholder="What should change here?"
        rows={2}
        className="selectable block w-full resize-none bg-transparent text-base outline-none placeholder:text-tertiary"
      />
      <div className="mt-1.5 flex justify-end gap-2">
        <Button size="sm" onPress={onCancel}>
          Cancel
        </Button>
        <Button
          size="sm"
          variant="primary"
          icon={<MessageSquarePlus size={11} />}
          isDisabled={!text.trim()}
          onPress={() => onSave(text)}
        >
          Comment
        </Button>
      </div>
    </div>
  );
}

export function CommentNote({ sessionId, comment: c }: { sessionId: string; comment: Comment }) {
  return (
    <div className="m-2 flex items-start gap-2 rounded-card border border-accent/30 bg-accent-soft px-3 py-2 font-sans text-base">
      <span className="selectable min-w-0 flex-1 whitespace-pre-wrap">{c.text}</span>
      <button
        type="button"
        aria-label="Delete comment"
        onClick={() => useComments.getState().remove(sessionId, c.id)}
        className="shrink-0 text-tertiary hover:text-bad"
      >
        <Trash2 size={12} />
      </button>
    </div>
  );
}
