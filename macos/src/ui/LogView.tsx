import { useEffect, useRef } from "react";
import { cleanLog } from "@/lib/format";

/** A scrolling, selectable log that follows its tail unless you scroll up. */
export function LogView({
  text,
  className = "",
  empty = "nothing logged yet",
}: {
  text: string;
  className?: string;
  empty?: string;
}) {
  const ref = useRef<HTMLPreElement>(null);
  const pinned = useRef(true);
  useEffect(() => {
    const el = ref.current;
    if (el && pinned.current && text) el.scrollTop = el.scrollHeight;
  }, [text]);
  const lines = cleanLog(text).split("\n");
  return (
    <pre
      ref={ref}
      onScroll={(e) => {
        const el = e.currentTarget;
        pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      }}
      className={`selectable overflow-auto rounded-ctl bg-[color-mix(in_oklch,var(--surface)_70%,black_8%)] p-3 font-mono text-xs leading-relaxed text-label ${className}`}
    >
      {text.trim() === "" ? (
        <span className="text-tertiary">{empty}</span>
      ) : (
        lines.map((l, i) => (
          <div
            key={i}
            className={/FAILED|error/i.test(l) ? "text-bad" : /^==>|done$/.test(l) ? "text-ok" : ""}
          >
            {l || " "}
          </div>
        ))
      )}
    </pre>
  );
}
