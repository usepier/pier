import { CircleAlert, CircleCheck, Info, X } from "lucide-react";
import { useToasts } from "@/state/toasts";

const icons = {
  ok: <CircleCheck size={15} className="shrink-0 text-ok" />,
  bad: <CircleAlert size={15} className="shrink-0 text-bad" />,
  info: <Info size={15} className="shrink-0 text-secondary" />,
} as const;

/** Transient messages in the window's lower right. */
export function Toasts() {
  const { items, dismiss } = useToasts();
  return (
    <div
      className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-[340px] flex-col items-end gap-2"
      aria-live="polite"
    >
      {items.map((t) => (
        <div
          key={t.id}
          role={t.tone === "bad" ? "alert" : "status"}
          className="pop-in group pointer-events-auto flex max-w-full items-start gap-2.5 rounded-[10px] border border-strong bg-popover py-2.5 pr-2.5 pl-3 text-base shadow-(--shadow-pop)"
        >
          <span className="mt-px">{icons[t.tone]}</span>
          <div className="selectable min-w-0 flex-1 break-words leading-snug">{t.text}</div>
          <button
            type="button"
            aria-label="Dismiss"
            onClick={() => dismiss(t.id)}
            className="-my-0.5 rounded-[5px] p-0.5 text-tertiary opacity-0 transition-opacity group-hover:opacity-100 hover:bg-hover hover:text-label focus-visible:opacity-100"
          >
            <X size={13} />
          </button>
        </div>
      ))}
    </div>
  );
}
