import type { ReactElement, ReactNode } from "react";
import { Tooltip as AriaTooltip, TooltipTrigger } from "react-aria-components";

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
      <AriaTooltip
        offset={6}
        className="pop-in max-w-[320px] rounded-[6px] bg-popover px-2 py-1 text-sm text-label shadow-(--shadow-pop)"
      >
        {content}
      </AriaTooltip>
    </TooltipTrigger>
  );
}
