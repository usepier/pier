// How a chat runs, in one picker: the model (every provider's, searchable),
// its effort, and the fast mode. A model of another provider than a
// running chat's starts a new chat with it.
import { ArrowUpRight, Check, ChevronDown, ChevronRight, Search, Zap } from "lucide-react";
import { useMemo, useState } from "react";
import { Button as AriaButton, Dialog, DialogTrigger, Popover } from "react-aria-components";
import type { ModelInfo } from "@/api/protocol.gen";
import { AgentGlyph } from "@/features/session/SessionCenter";
import { useChatDefaults } from "@/state/settings";
import { Switch } from "@/ui/Switch";
import { useModels } from "./Composer";

const effortNames: Record<string, string> = { xhigh: "Extra high" };

/** An effort level as the picker writes it: "Extra high", "Ultra". */
export function effortLabel(e: string) {
  return effortNames[e] ?? e.charAt(0).toUpperCase() + e.slice(1);
}

/** Why an agent's fast mode is off though asked for: its reason, in its own words. */
const fastOffText: Record<string, string> = {
  preference: "Fast mode has been disabled by your organization.",
  free: "Fast mode requires a paid subscription.",
  extra_usage_disabled: "Fast mode needs extra usage turned on.",
  model_not_allowed: "Fast mode isn't available on this model.",
  network_error: "Fast mode unavailable due to network connectivity issues.",
};

const providerNames: Record<string, string> = { claude: "Claude", codex: "Codex" };

export interface ModelChoice {
  agent: string;
  model: string;
  effort: string;
  fast: boolean;
  /** Why fast mode is off though asked for, as the agent says. */
  fastOff?: string;
}

const modelName = (m: ModelInfo) => m.name || m.id;

const row =
  "flex h-8 w-full cursor-pointer items-center gap-2 rounded-[6px] px-2 text-left text-base outline-none hover:bg-hover focus-visible:bg-hover";

export function ModelPicker({
  value,
  onChange,
  lockedAgent,
  onOtherAgent,
  disabled,
  withFast = true,
  placement = "top start",
}: {
  value: ModelChoice;
  onChange: (v: Partial<ModelChoice>) => void;
  /** A running chat's agent: models of others start a new chat (onOtherAgent). */
  lockedAgent?: string;
  onOtherAgent?: (agent: string, model: string) => void;
  disabled?: boolean;
  withFast?: boolean;
  placement?: "top start" | "bottom end";
}) {
  const claude = useModels("claude");
  const codex = useModels("codex");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const all = useMemo(
    () => [
      ...(claude.data?.models ?? []).map((m) => ({ agent: "claude", m })),
      ...(codex.data?.models ?? []).map((m) => ({ agent: "codex", m })),
    ],
    [claude.data, codex.data],
  );
  // A 1M-context run is its model's id with [1m]: one model, a variant on.
  const base = value.model.replace(/\[1m\]$/, "");
  // No model picked runs the agent's default: shown as the model it is.
  const current =
    all.find((x) => x.agent === value.agent && x.m.id === base && base !== "") ??
    all.find((x) => x.agent === value.agent && x.m.default) ??
    all.find((x) => x.agent === value.agent);
  const long = !!current?.m.long && (value.model ? value.model.endsWith("[1m]") : !!current.m.longDefault);
  // One default: the model new chats run (the settings'), not each agent's.
  const def = useChatDefaults()();
  const defBase = def.model.replace(/\[1m\]$/, "");
  const isDefault = (agent: string, m: ModelInfo) =>
    agent === def.agent && (defBase ? m.id === defBase : !!m.default);
  const efforts = current?.m.efforts ?? [];
  const effort = value.effort || current?.m.defaultEffort || "";
  const shown = all.filter(({ agent, m }) =>
    `${providerNames[agent]} ${modelName(m)}`.toLowerCase().includes(query.trim().toLowerCase()),
  );
  const pick = (agent: string, m: ModelInfo) => {
    // A model with a 1M variant keeps the 1M choice as it was.
    const model = m.long && long ? `${m.id}[1m]` : m.id;
    if (lockedAgent && agent !== lockedAgent) onOtherAgent?.(agent, model);
    else onChange(agent === value.agent ? { model } : { agent, model, effort: "" });
    setOpen(false);
  };
  return (
    <DialogTrigger isOpen={open} onOpenChange={setOpen}>
      <AriaButton
        aria-label="Model"
        isDisabled={disabled}
        className="inline-flex h-7 items-center gap-1.5 rounded-[7px] px-2 text-sm outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-accent/50 disabled:opacity-50"
      >
        <AgentGlyph agent={value.agent} />
        <span className="text-label">{current ? modelName(current.m) : "Model"}</span>
        {long && <span className="text-tertiary">1M</span>}
        {effort && <span className="text-tertiary">{effortLabel(effort)}</span>}
        {value.fast && current?.m.fast && !value.fastOff && (
          <Zap size={12} className="text-warn" aria-label="fast" />
        )}
        <ChevronDown size={12} className="text-tertiary" />
      </AriaButton>
      <Popover placement={placement} offset={6} className="pop-in">
        <Dialog
          aria-label="Model, effort and speed"
          className="w-[300px] rounded-card bg-popover p-1 shadow-(--shadow-pop) outline-none"
        >
          <div className="flex items-center gap-2 px-2 pt-1 pb-1.5">
            <Search size={13} className="text-tertiary" />
            <input
              autoFocus
              aria-label="Search models"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search models"
              className="h-7 min-w-0 flex-1 bg-transparent text-base outline-none placeholder:text-tertiary"
            />
          </div>
          <div className="mx-1 h-px bg-separator" />
          <div role="listbox" aria-label="Models" className="max-h-[260px] overflow-y-auto py-1">
            {shown.map(({ agent, m }) => {
              const on = current?.agent === agent && current.m.id === m.id;
              const elsewhere = !!lockedAgent && agent !== lockedAgent;
              return (
                <button
                  type="button"
                  role="option"
                  aria-selected={on}
                  key={`${agent}/${m.id}`}
                  onClick={() => pick(agent, m)}
                  className={row}
                  title={elsewhere ? `Starts a new ${providerNames[agent]} chat` : m.description}
                >
                  <AgentGlyph agent={agent} />
                  <span className="min-w-0 flex-1 truncate">{modelName(m)}</span>
                  {isDefault(agent, m) && <span className="text-xs text-tertiary">default</span>}
                  {on ? (
                    <Check size={14} className="text-label" />
                  ) : elsewhere ? (
                    <ArrowUpRight size={14} className="text-tertiary" />
                  ) : null}
                </button>
              );
            })}
            {shown.length === 0 && <div className="px-2 py-1.5 text-sm text-tertiary">No model matches.</div>}
          </div>
          <div className="mx-1 h-px bg-separator" />
          <div className="py-1">
            {efforts.length > 0 && (
              <EffortRow
                efforts={efforts}
                value={value.effort}
                defaultEffort={current?.m.defaultEffort}
                onChange={(e) => {
                  onChange({ effort: e });
                  setOpen(false);
                }}
              />
            )}
            {current?.m.long && (
              <div className="flex h-8 items-center gap-2 px-2 text-base">
                <span className="flex-1">1M context</span>
                <Switch
                  isSelected={long}
                  onChange={(on) => onChange({ model: on ? `${current.m.id}[1m]` : current.m.id })}
                  label="1M context"
                />
              </div>
            )}
            {withFast && current?.m.fast && (
              <div className="px-2 py-1">
                <div className="flex h-7 items-center gap-2 text-base">
                  <span className="flex-1">Fast</span>
                  <Switch isSelected={value.fast} onChange={(fast) => onChange({ fast })} label="Fast" />
                </div>
                {value.fast && value.fastOff && (
                  <div className="pb-1 text-xs text-warn">
                    {fastOffText[value.fastOff] ?? "Fast mode is off for now."}
                  </div>
                )}
              </div>
            )}
          </div>
        </Dialog>
      </Popover>
    </DialogTrigger>
  );
}

/** Effort, opening its levels to the side. */
function EffortRow({
  efforts,
  value,
  defaultEffort,
  onChange,
}: {
  efforts: string[];
  value: string;
  defaultEffort?: string;
  onChange: (e: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const shown = value || defaultEffort || "";
  return (
    <DialogTrigger isOpen={open} onOpenChange={setOpen}>
      <AriaButton className={row}>
        <span className="flex-1">Effort</span>
        <span className="text-tertiary">{shown ? effortLabel(shown) : "Default"}</span>
        <ChevronRight size={13} className="text-tertiary" />
      </AriaButton>
      <Popover placement="end top" offset={8} className="pop-in">
        <Dialog
          aria-label="Effort"
          className="w-[180px] rounded-card bg-popover p-1 shadow-(--shadow-pop) outline-none"
        >
          {efforts.map((e) => (
            <button
              type="button"
              key={e}
              onClick={() => {
                onChange(e);
                setOpen(false);
              }}
              className={row}
            >
              <span className="flex-1">{effortLabel(e)}</span>
              {shown === e && <Check size={14} />}
            </button>
          ))}
        </Dialog>
      </Popover>
    </DialogTrigger>
  );
}
