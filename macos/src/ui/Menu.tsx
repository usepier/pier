import type { ReactNode } from "react";
import {
  Menu as AriaMenu,
  MenuItem as AriaMenuItem,
  MenuTrigger,
  Popover,
  Separator,
} from "react-aria-components";

export interface MenuEntry {
  id: string;
  label: ReactNode;
  icon?: ReactNode;
  shortcut?: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onAction: () => void;
}

/** A button that opens a menu of actions. */
export function Menu({
  trigger,
  items,
  label,
  placement = "bottom end",
}: {
  trigger: ReactNode;
  items: (MenuEntry | "separator")[];
  label: string;
  placement?: "bottom end" | "bottom start" | "top start" | "top end";
}) {
  return (
    <MenuTrigger>
      {trigger}
      <Popover placement={placement} offset={4} className="pop-in">
        <AriaMenu
          aria-label={label}
          className="min-w-[200px] rounded-card bg-popover p-1 shadow-(--shadow-pop) outline-none"
        >
          {items.map((it, i) =>
            it === "separator" ? (
              <Separator key={`sep-${i}`} className="mx-1 my-1 h-px bg-separator" />
            ) : (
              <AriaMenuItem
                key={it.id}
                id={it.id}
                onAction={it.onAction}
                isDisabled={it.disabled}
                className={`flex cursor-pointer items-center gap-2 rounded-[6px] px-2 py-1.5 text-base outline-none data-disabled:cursor-default data-disabled:opacity-40 data-focused:bg-hover ${it.danger ? "text-bad" : ""}`}
              >
                {it.icon && <span className="shrink-0 text-tertiary">{it.icon}</span>}
                <span className="flex-1 truncate">{it.label}</span>
                {it.shortcut && <span className="text-sm text-tertiary">{it.shortcut}</span>}
              </AriaMenuItem>
            ),
          )}
        </AriaMenu>
      </Popover>
    </MenuTrigger>
  );
}
