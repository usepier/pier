import { X } from "lucide-react";
import { useToasts } from "@/state/toasts";

/** Transient messages in the window's lower right. */
export function Toasts() {
  const { items, dismiss } = useToasts();
  return (
    <div
      className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-80 flex-col gap-2"
      aria-live="polite"
    >
      {items.map((t) => (
        <div
          key={t.id}
          className={`pointer-events-auto flex items-start gap-2 rounded-card border border-separator bg-surface px-3 py-2 text-sm shadow-lg ${t.tone === "bad" ? "border-l-[3px] border-l-bad" : t.tone === "ok" ? "border-l-[3px] border-l-ok" : ""}`}
        >
          <div className="selectable min-w-0 flex-1 break-words">{t.text}</div>
          <button
            type="button"
            aria-label="Dismiss"
            onClick={() => dismiss(t.id)}
            className="text-tertiary hover:text-label"
          >
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  );
}
