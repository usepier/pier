import type { ComponentProps, ReactNode } from "react";
import { Button as AriaButton } from "react-aria-components";
import { useBusy } from "@/lib/useBusy";
import { Spinner } from "./Spinner";

type Variant = "primary" | "secondary" | "plain" | "danger";

const variants: Record<Variant, string> = {
  primary: "bg-accent-fill text-on-accent hover:brightness-110 data-pressed:brightness-95",
  secondary: "border border-strong bg-control text-label hover:bg-hover data-pressed:bg-selected",
  plain: "text-secondary hover:bg-hover hover:text-label data-pressed:bg-selected",
  danger: "bg-bad/90 text-white hover:bg-bad data-pressed:brightness-95",
};

export interface ButtonProps extends Omit<ComponentProps<typeof AriaButton>, "children" | "className"> {
  variant?: Variant;
  size?: "sm" | "md";
  icon?: ReactNode;
  children?: ReactNode;
  className?: string;
}

/** A button: primary for the one action that moves things on, secondary
 * for the rest, plain inside toolbars and rows. isPending says its action
 * runs: a spinner in its icon's place, and no second press. */
export function Button({
  variant = "secondary",
  size = "md",
  icon,
  children,
  className = "",
  ...rest
}: ButtonProps) {
  const sz = size === "sm" ? "h-[26px] px-2.5 text-sm gap-1.5" : "h-[30px] px-3 text-base gap-1.5";
  return (
    <AriaButton
      {...rest}
      className={`inline-flex cursor-pointer items-center justify-center rounded-ctl font-medium whitespace-nowrap select-none outline-none transition-[filter,background-color,color] duration-100 disabled:cursor-default disabled:opacity-40 data-pending:cursor-progress focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent ${sz} ${variants[variant]} ${className}`}
    >
      {rest.isPending ? <Spinner size={size === "sm" ? 11 : 12} /> : icon}
      {children}
    </AriaButton>
  );
}

/**
 * A Button whose action takes a while (it asks pier for something): pending
 * — a spinner, no second press — until the action is done.
 */
export function ActionButton({
  action,
  ...props
}: Omit<ButtonProps, "onPress" | "isPending"> & { action: () => Promise<unknown> }) {
  const [busy, run] = useBusy();
  return <Button {...props} isPending={busy} onPress={() => void run(action)} />;
}

/** A square, borderless button for an icon: toolbars, rows, cards. */
export function IconButton({
  label,
  children,
  className = "",
  size = "md",
  ...rest
}: { label: string; children: ReactNode; className?: string; size?: "sm" | "md" } & Omit<
  ComponentProps<typeof AriaButton>,
  "children" | "className"
>) {
  return (
    <AriaButton
      aria-label={label}
      {...rest}
      className={`inline-flex shrink-0 cursor-pointer items-center justify-center rounded-ctl text-tertiary outline-none transition-colors duration-100 hover:bg-hover hover:text-label data-pressed:bg-selected disabled:cursor-default disabled:opacity-35 data-pending:cursor-progress focus-visible:outline-2 focus-visible:outline-accent ${size === "sm" ? "size-6" : "size-7"} ${className}`}
    >
      {rest.isPending ? <Spinner size={11} /> : children}
    </AriaButton>
  );
}
