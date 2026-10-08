import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Info } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import {
  Button as AriaButton,
  Dialog,
  DialogTrigger,
  Input,
  ListBox,
  ListBoxItem,
  Popover,
  Select,
  SelectValue,
  TextField,
} from "react-aria-components";
import { rpc } from "@/api/client";
import type { Setting, Settings } from "@/api/protocol.gen";
import { accentColor, applyAccent } from "@/lib/theme";
import { attempt, toast } from "@/state/toasts";
import { Empty } from "@/ui/Empty";
import { Spinner } from "@/ui/Spinner";

/** The settings page: pier's own settings schema, one grouped form. */
export function SettingsPage() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["settings"], queryFn: () => rpc.call("settings.get", {}) });
  const save = async (key: string, value: string) => {
    const next = await attempt(() => rpc.call("settings.set", { key, value }));
    if (!next) return;
    qc.setQueryData(["settings"], next);
    if (key === "theme") applyAccent(value);
    toast("Saved", "ok");
  };
  if (q.isLoading) return <Empty title="Settings" detail={<Spinner />} />;
  if (q.error) return <Empty title="Settings" detail={(q.error as Error).message} />;
  const data = q.data as Settings;
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-2xl flex-col gap-6 p-6">
        <h1 className="text-xl font-semibold">Settings</h1>
        {data.groups.map((g) => {
          const rows = data.settings.filter((s) => s.group === g.key);
          if (!rows.length) return null;
          return (
            <section key={g.key}>
              <h2 className="mb-2 px-1 text-xs font-semibold text-secondary">{g.title}</h2>
              <div className="divide-y divide-separator rounded-card border border-separator bg-raised/60">
                {rows.map((s) => (
                  <Row key={s.key} setting={s} onSave={save} />
                ))}
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
}

function Row({
  setting: s,
  onSave,
}: {
  setting: Setting;
  onSave: (key: string, value: string) => Promise<void>;
}) {
  return (
    <div className="flex items-center gap-4 px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1 text-base first-letter:uppercase">
          {s.label}
          {s.detail && <Detail setting={s} />}
        </div>
        {s.hint && <div className="text-xs text-secondary">{s.hint}</div>}
      </div>
      <div className="w-56 shrink-0">
        <Control setting={s} onSave={onSave} />
      </div>
    </div>
  );
}

function Detail({ setting: s }: { setting: Setting }) {
  return (
    <DialogTrigger>
      <AriaButton aria-label={`About ${s.label}`} className="text-tertiary outline-none hover:text-label">
        <Info size={12} />
      </AriaButton>
      <Popover className="max-w-sm rounded-card border border-separator bg-surface p-3 text-sm shadow-xl outline-none">
        <Dialog className="outline-none">
          {s.detail.split("\n").map((line, i) => (
            <p key={i} className="mb-1.5 last:mb-0">
              {codeSpans(line)}
            </p>
          ))}
          {s.default && <p className="mt-2 text-xs text-secondary">Default: {s.default}</p>}
        </Dialog>
      </Popover>
    </DialogTrigger>
  );
}

function codeSpans(text: string): ReactNode[] {
  return text.split(/(`[^`]+`)/).map((part, i) =>
    part.startsWith("`") ? (
      <code key={i} className="rounded bg-selected px-1 font-mono text-xs">
        {part.slice(1, -1)}
      </code>
    ) : (
      part
    ),
  );
}

const CUSTOM = "\u0000custom";

function Control({
  setting: s,
  onSave,
}: {
  setting: Setting;
  onSave: (key: string, value: string) => Promise<void>;
}) {
  const [custom, setCustom] = useState(false);
  const machines = useQuery({
    queryKey: ["catalog", s.value],
    queryFn: () => rpc.call("settings.machines", { current: s.value }),
    enabled: s.kind === "machine",
  });
  if (s.kind === "text" || custom) return <TextControl setting={s} onSave={onSave} />;
  const options =
    s.kind === "machine"
      ? (machines.data ?? []).map((m) => ({
          value: m.type,
          label: m.type,
          desc: `${m.cpu} vCPU · ${m.mem} GiB · ${m.cost}`,
        }))
      : (s.options ?? []);
  const known = options.some((o) => o.value === s.value);
  return (
    <Select
      aria-label={s.label}
      selectedKey={known ? s.value : null}
      onSelectionChange={(k) => {
        if (k === CUSTOM) setCustom(true);
        else if (k !== null && String(k) !== s.value) void onSave(s.key, String(k));
      }}
    >
      <AriaButton className="flex h-7 w-full items-center justify-between gap-2 rounded-ctl border border-separator bg-control px-2 text-left text-base outline-none focus-visible:ring-2 focus-visible:ring-accent/40">
        <SelectValue className="flex min-w-0 items-center gap-2 truncate">
          {({ isPlaceholder }) =>
            isPlaceholder ? <span className="truncate">{s.display || s.value}</span> : undefined
          }
        </SelectValue>
        <span aria-hidden="true" className="text-tertiary">
          ⌄
        </span>
      </AriaButton>
      <Popover className="max-h-80 w-72 overflow-auto rounded-card border border-separator bg-surface p-1 shadow-xl outline-none">
        <ListBox className="outline-none">
          {options.map((o) => (
            <ListBoxItem
              key={o.value}
              id={o.value}
              textValue={o.label || o.value}
              className="flex cursor-default items-center gap-2 rounded-ctl px-2 py-1 outline-none data-focused:bg-accent data-focused:text-on-accent"
            >
              {s.key === "theme" && (
                <span className="size-3 rounded-full" style={{ background: accentColor(o.value) }} />
              )}
              <span className="flex-1 text-base">{o.label || o.value}</span>
              {o.desc && <span className="text-xs opacity-70">{o.desc}</span>}
            </ListBoxItem>
          ))}
          {!s.noCustom && (
            <ListBoxItem
              id={CUSTOM}
              textValue="Custom"
              className="rounded-ctl px-2 py-1 text-base text-secondary outline-none data-focused:bg-accent data-focused:text-on-accent"
            >
              Custom…
            </ListBoxItem>
          )}
        </ListBox>
      </Popover>
    </Select>
  );
}

function TextControl({
  setting: s,
  onSave,
}: {
  setting: Setting;
  onSave: (key: string, value: string) => Promise<void>;
}) {
  const [v, setV] = useState(s.value);
  useEffect(() => setV(s.value), [s.value]);
  const commit = () => {
    if (v !== s.value) void onSave(s.key, v.trim());
  };
  return (
    <TextField aria-label={s.label} value={v} onChange={setV} onBlur={commit}>
      <Input
        placeholder={s.empty || s.default}
        onKeyDown={(e) => e.key === "Enter" && commit()}
        className="selectable h-7 w-full rounded-ctl border border-separator bg-control px-2 text-base outline-none placeholder:text-tertiary focus:border-accent focus:ring-2 focus:ring-accent/25"
      />
    </TextField>
  );
}
