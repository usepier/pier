import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { Input, TextField } from "react-aria-components";
import { rpc } from "@/api/client";
import type { Setting } from "@/api/protocol.gen";
import { accentColor } from "@/lib/theme";
import { type Option, Select } from "@/ui/Select";
import { Switch } from "@/ui/Switch";
import type { Item } from "./model";

export type Save = (key: string, value: string) => Promise<void>;

/** `code` spans in a setting's words. */
export function codeSpans(text: string): ReactNode[] {
  return text.split(/(`[^`]+`)/).map((part, i) =>
    part.startsWith("`") ? (
      <code key={i} className="rounded-[4px] border border-separator bg-raised px-1 font-mono text-[0.88em]">
        {part.slice(1, -1)}
      </code>
    ) : (
      part
    ),
  );
}

const capital = (s: string) => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

/** A pier setting as a settings row: its label, what it does, its control. */
export function pierItem(s: Setting, save: Save): Item {
  const [first, ...rest] = (s.detail || s.hint).split("\n");
  const hint = [rest.join(" "), s.default ? `Default: ${s.default}` : ""].filter(Boolean).join(" · ");
  return {
    id: s.key,
    title: capital(s.label),
    text: `${s.hint} ${s.detail} ${s.key}`,
    description: codeSpans(first ?? ""),
    hint: hint ? codeSpans(hint) : undefined,
    control: <PierControl setting={s} save={save} />,
  };
}

const CUSTOM = "\u0000custom";

function isSwitch(s: Setting) {
  const vals = (s.options ?? []).map((o) => o.value).sort();
  return s.noCustom && vals.length === 2 && vals[0] === "false" && vals[1] === "true";
}

export function PierControl({ setting: s, save }: { setting: Setting; save: Save }) {
  const [custom, setCustom] = useState(false);
  const machines = useQuery({
    queryKey: ["catalog", s.value],
    queryFn: () => rpc.call("settings.machines", { current: s.value }),
    enabled: s.kind === "machine",
  });
  if (isSwitch(s)) {
    return (
      <Switch
        label={s.label}
        isSelected={s.value === "true"}
        onChange={(on) => void save(s.key, on ? "true" : "false")}
      />
    );
  }
  if (s.kind === "text" || custom)
    return <TextControl setting={s} save={save} onDone={() => setCustom(false)} />;
  const options: Option[] =
    s.kind === "machine"
      ? (machines.data ?? []).map((m) => ({
          value: m.type,
          label: m.type,
          description: `${m.cpu} vCPU · ${m.mem} GiB · ${m.cost}`,
        }))
      : (s.options ?? []).map((o) => ({
          value: o.value,
          label: o.label || o.value || s.empty || "—",
          description: o.desc,
          icon:
            s.key === "theme" ? (
              <span className="block size-3 rounded-full" style={{ background: accentColor(o.value) }} />
            ) : undefined,
        }));
  if (!options.some((o) => o.value === s.value))
    options.unshift({ value: s.value, label: s.display || s.value || s.empty || "—" });
  if (!s.noCustom) options.push({ value: CUSTOM, label: "Custom…" });
  return (
    <Select
      label={s.label}
      width={240}
      value={s.value}
      options={options}
      onChange={(v) => {
        if (v === CUSTOM) setCustom(true);
        else if (v !== s.value) void save(s.key, v);
      }}
    />
  );
}

function TextControl({ setting: s, save, onDone }: { setting: Setting; save: Save; onDone: () => void }) {
  const [v, setV] = useState(s.value);
  useEffect(() => setV(s.value), [s.value]);
  const commit = () => {
    if (v.trim() !== s.value) void save(s.key, v.trim());
    onDone();
  };
  return (
    <TextField aria-label={s.label} value={v} onChange={setV} onBlur={commit}>
      <Input
        placeholder={s.empty || s.default}
        onKeyDown={(e) => e.key === "Enter" && commit()}
        className="selectable h-[30px] w-[240px] rounded-ctl border border-strong bg-control px-2.5 text-base outline-none transition-colors placeholder:text-tertiary hover:border-quaternary focus:border-accent focus:ring-2 focus:ring-accent/20"
      />
    </TextField>
  );
}
