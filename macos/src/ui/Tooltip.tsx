import { type ReactElement, type ReactNode, useEffect, useRef, useState } from "react";
import { Tooltip as AriaTooltip, TooltipTrigger } from "react-aria-components";
import { createPortal } from "react-dom";

const tipClass =
  "pop-in max-w-[320px] rounded-[6px] bg-popover px-2 py-1 text-sm text-label shadow-(--shadow-pop)";

/** A short label that shows on hover, for icon buttons and truncated text. */
export function Tip({
  content,
  children,
  delay = 500,
}: {
  content: ReactNode;
  children: ReactElement;
  delay?: number;
}) {
  return (
    <TooltipTrigger delay={delay} closeDelay={0}>
      {children}
      <AriaTooltip offset={6} className={tipClass}>
        {content}
      </AriaTooltip>
    </TooltipTrigger>
  );
}

/**
 * A tooltip on something that isn't a control (a status icon inside a row
 * that is one): it shows while the pointer rests on it. What it says must
 * also be in the control's label, for the keyboard and VoiceOver.
 */
export function HoverTip({
  content,
  children,
  className = "",
}: {
  content: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLSpanElement>(null);
  const timer = useRef(0);
  const [at, setAt] = useState<DOMRect | null>(null);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  // Centered under it, kept inside the window.
  const left = at ? Math.min(Math.max(at.left + at.width / 2, 170), window.innerWidth - 170) : 0;
  return (
    <span
      ref={ref}
      className={`inline-flex shrink-0 items-center ${className}`}
      onPointerEnter={() => {
        timer.current = window.setTimeout(() => setAt(ref.current?.getBoundingClientRect() ?? null), 350);
      }}
      onPointerLeave={() => {
        window.clearTimeout(timer.current);
        setAt(null);
      }}
    >
      {children}
      {at &&
        createPortal(
          <div
            role="tooltip"
            style={{ left, top: at.bottom + 6 }}
            className={`pointer-events-none fixed z-50 w-max -translate-x-1/2 ${tipClass}`}
          >
            {content}
          </div>,
          document.body,
        )}
    </span>
  );
}
