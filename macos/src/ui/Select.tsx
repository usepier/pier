import { Check, ChevronsUpDown } from "lucide-react";
import type { ReactNode } from "react";
import {
  Button as AriaButton,
  Select as AriaSelect,
  ListBox,
  ListBoxItem,
  Popover,
  SelectValue,
} from "react-aria-components";

export interface Option<V extends string = string> {
  value: V;
  label: ReactNode;
  /** A line under the label in the list. */
  description?: ReactNode;
  icon?: ReactNode;
  disabled?: boolean;
}

/** A dropdown that picks one of a few values. */
export function Select<V extends string>({
  value,
  onChange,
  options,
  label,
  width,
  size = "md",
  variant = "bordered",
  isDisabled,
  placement = "bottom end",
}: {
  value: V;
  onChange: (v: V) => void;
  options: Option<V>[];
  label: string;
  width?: number;
  size?: "sm" | "md";
  /** bordered: a settings control; ghost: a chip inside a toolbar. */
  variant?: "bordered" | "ghost";
  isDisabled?: boolean;
  placement?: "bottom end" | "bottom start" | "top start" | "top end";
}) {
  const box =
    variant === "bordered"
      ? "border border-strong bg-control px-2.5 hover:border-quaternary"
      : "px-2 text-secondary hover:bg-hover hover:text-label";
  return (
    <AriaSelect
      aria-label={label}
      selectedKey={value}
      onSelectionChange={(k) => onChange(k as V)}
      isDisabled={isDisabled}
      className="inline-flex"
      style={width ? { width } : undefined}
    >
      <AriaButton
        className={`inline-flex w-full cursor-pointer items-center gap-1.5 rounded-ctl text-left outline-none transition-colors duration-100 disabled:cursor-default disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-accent ${size === "sm" ? "h-[26px] text-sm" : "h-[30px] text-base"} ${box}`}
      >
        <SelectValue className="flex min-w-0 flex-1 items-center gap-1.5">
          {() => {
            const o = options.find((x) => x.value === value);
            return (
              <>
                {o?.icon && <span className="shrink-0 text-tertiary">{o.icon}</span>}
                <span className="truncate">{o?.label ?? value}</span>
              </>
            );
          }}
        </SelectValue>
        <ChevronsUpDown size={12} className="shrink-0 text-tertiary" />
      </AriaButton>
      <Popover placement={placement} offset={4} className="pop-in min-w-(--trigger-width)">
        <OptionList options={options} />
      </Popover>
    </AriaSelect>
  );
}

function OptionList<V extends string>({ options }: { options: Option<V>[] }) {
  return (
    <ListBox
      items={options.map((o) => ({ ...o, id: o.value }))}
      className="max-h-[360px] overflow-auto rounded-card bg-popover p-1 shadow-(--shadow-pop) outline-none"
    >
      {(o) => (
        <ListBoxItem
          id={o.value}
          textValue={typeof o.label === "string" ? o.label : o.value}
          isDisabled={o.disabled}
          className="group flex cursor-pointer items-start gap-2 rounded-[6px] px-2 py-1.5 text-base outline-none data-disabled:cursor-default data-disabled:opacity-40 data-focused:bg-hover"
        >
          {o.icon && <span className="mt-0.5 shrink-0 text-tertiary">{o.icon}</span>}
          <span className="min-w-0 flex-1">
            <span className="block truncate">{o.label}</span>
            {o.description && (
              <span data-desc className="block text-sm text-tertiary">
                {o.description}
              </span>
            )}
          </span>
          <Check
            size={13}
            className="mt-0.5 shrink-0 text-accent opacity-0 group-data-selected:opacity-100"
          />
        </ListBoxItem>
      )}
    </ListBox>
  );
}
