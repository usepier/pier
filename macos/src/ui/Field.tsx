import type { ReactNode } from "react";
import { Input, Label, Text, TextField } from "react-aria-components";

/** A labeled text input with an optional hint or error underneath. */
export function Field({
  label,
  value,
  onChange,
  placeholder,
  hint,
  error,
  mono,
  autoFocus,
  onEnter,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  hint?: ReactNode;
  error?: string | null;
  mono?: boolean;
  autoFocus?: boolean;
  onEnter?: () => void;
}) {
  return (
    <TextField
      value={value}
      onChange={onChange}
      isInvalid={!!error}
      autoFocus={autoFocus}
      className="flex flex-col gap-1"
    >
      <Label className="text-xs font-medium text-secondary">{label}</Label>
      <Input
        placeholder={placeholder}
        onKeyDown={(e) => {
          if (e.key === "Enter" && onEnter) onEnter();
        }}
        className={`selectable h-7 rounded-ctl border border-separator bg-control px-2 text-base text-label outline-none placeholder:text-tertiary focus:border-accent focus:ring-2 focus:ring-accent/25 data-invalid:border-bad ${mono ? "font-mono text-sm" : ""}`}
      />
      {error ? (
        <Text slot="errorMessage" className="text-xs text-bad">
          {error}
        </Text>
      ) : hint ? (
        <Text slot="description" className="text-xs text-secondary">
          {hint}
        </Text>
      ) : null}
    </TextField>
  );
}
