import type { ComponentProps, ReactNode } from "react";
import { Button as AriaButton } from "react-aria-components";

type Variant = "primary" | "secondary" | "plain" | "danger";

const variants: Record<Variant, string> = {
  primary: "bg-accent text-on-accent shadow-sm hover:brightness-105 pressed:brightness-95",
  secondary:
    "bg-control text-label border border-separator shadow-[0_0.5px_1px_rgb(0_0_0/0.08)] hover:bg-hover pressed:bg-selected",
  plain: "text-label hover:bg-hover pressed:bg-selected",
  danger: "bg-bad text-white shadow-sm hover:brightness-105 pressed:brightness-95",
};

export interface ButtonProps extends Omit<ComponentProps<typeof AriaButton>, "children" | "className"> {
  variant?: Variant;
  size?: "sm" | "md";
  icon?: ReactNode;
  children?: ReactNode;
  className?: string;
}

/** A Mac-sized push button. */
export function Button({
  variant = "secondary",
  size = "md",
  icon,
  children,
  className = "",
  ...rest
}: ButtonProps) {
  const sz = size === "sm" ? "h-[22px] px-2 text-xs gap-1" : "h-7 px-3 text-base gap-1.5";
  return (
    <AriaButton
      {...rest}
      className={`inline-flex items-center justify-center rounded-ctl font-medium whitespace-nowrap select-none outline-none transition-[filter,background-color] disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-accent ${sz} ${variants[variant]} ${className}`}
    >
      {icon}
      {children}
    </AriaButton>
  );
}

/** A square, borderless toolbar button. */
export function IconButton({
  label,
  children,
  className = "",
  ...rest
}: { label: string; children: ReactNode; className?: string } & Omit<
  ComponentProps<typeof AriaButton>,
  "children" | "className"
>) {
  return (
    <AriaButton
      aria-label={label}
      {...rest}
      className={`inline-flex size-7 items-center justify-center rounded-ctl text-secondary outline-none hover:bg-hover hover:text-label pressed:bg-selected disabled:opacity-35 focus-visible:outline-2 focus-visible:outline-accent ${className}`}
    >
      {children}
    </AriaButton>
  );
}
