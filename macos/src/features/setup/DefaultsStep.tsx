// Setup's defaults step: what new sessions get, with what it costs, and
// what travels into them.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Terminal } from "lucide-react";
import { useCallback, useState } from "react";
import { rpc } from "@/api/client";
import type { LocalPurpose, SetupDraft, SetupState } from "@/api/protocol.gen";
import { Button } from "@/ui/Button";
import { LocalTerminalSheet } from "./LocalTerminalSheet";
import { Nav } from "./parts";

const disks = [20, 40, 80, 160];
const idles = ["15m", "30m", "1h", "2h", "never"];
const caps = ["4h", "8h", "24h", "never"];
const speeds: [string, string][] = [
  ["fast", "Fast"],
  ["lean", "Lean"],
  ["minimal", "Minimal"],
];

export function DefaultsStep({
  st,
  draft,
  update,
  skills,
  setSkills,
  back,
  next,
}: {
  st: SetupState;
  draft: SetupDraft;
  update: (p: Partial<SetupDraft>) => void;
  skills: string[];
  setSkills: (s: string[]) => void;
  back: () => void;
  next: () => void;
}) {
  const defaults = useQuery({
    queryKey: ["setup-defaults", draft],
    queryFn: () => rpc.call("setup.defaults", draft),
    placeholderData: (prev) => prev,
  });
  const [terminal, setTerminal] = useState<LocalPurpose>();
  const qc = useQueryClient();
  // The token went into pier's settings: the field says it's saved.
  const onTokenCaptured = useCallback(() => void qc.invalidateQueries({ queryKey: ["setup"] }), [qc]);
  const note = (key: string) => defaults.data?.rows.find((r) => r.key === key)?.note;
  const select = (
    label: string,
    value: string,
    options: string[],
    onChange: (v: string) => void,
    render = (o: string) => o,
  ) => (
    <select
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-7 w-auto min-w-44 max-w-full rounded-ctl border border-separator bg-control px-2 text-base outline-none"
    >
      {options.map((o) => (
        <option key={o} value={o}>
          {render(o)}
        </option>
      ))}
    </select>
  );
  const machines = defaults.data?.machines ?? [];
  const row = (label: string, control: React.ReactNode, hint?: string) => (
    <div className="flex items-start gap-4 border-t border-separator px-3 py-2.5 first:border-t-0">
      <div className="w-28 shrink-0 pt-1 text-sm text-secondary">{label}</div>
      <div className="min-w-0 flex-1">
        {control}
        {hint && <div className="mt-1 text-xs text-secondary">{hint}</div>}
      </div>
    </div>
  );
  return (
    <section className="flex flex-col gap-4">
      <div className="text-sm text-secondary">
        What every new session gets. All of it changes later in Settings.
      </div>
      <div className="rounded-card border border-separator bg-raised/40">
        {row(
          "Machine",
          select(
            "Machine",
            draft.machine,
            machines.length ? machines.map((m) => m.type) : [draft.machine],
            (v) => update({ machine: v }),
            (t) => {
              const m = machines.find((x) => x.type === t);
              return m ? `${t} — ${m.cpu} vCPU · ${m.mem} GB · ${m.cost}` : t;
            },
          ),
          "Resize any session later.",
        )}
        {row(
          "Disk",
          select(
            "Disk",
            String(draft.diskGib),
            disks.map(String),
            (v) => update({ diskGib: Number(v) }),
            (d) => `${d} GiB`,
          ),
          note(draft.cloud === "gcp-gce" ? "gcp.disk_gib" : "aws.disk_gib"),
        )}
        {row(
          "Park after",
          select(
            "Park after",
            draft.idleTimeout,
            idles,
            (v) => update({ idleTimeout: v }),
            (v) => (v === "never" ? "never" : `${v} idle`),
          ),
          note("idle_timeout"),
        )}
        {row(
          "Runaway cap",
          select("Runaway cap", draft.unattendedCap, caps, (v) => update({ unattendedCap: v })),
          note("unattended_cap"),
        )}
        {row(
          "Speed",
          <fieldset
            aria-label="Speed"
            className="m-0 flex w-fit rounded-ctl border-0 bg-selected p-0.5 text-sm"
          >
            {speeds.map(([v, label]) => (
              <button
                key={v}
                type="button"
                aria-pressed={draft.speed === v}
                onClick={() => update({ speed: v })}
                className={`rounded-[4px] px-3 py-0.5 ${draft.speed === v ? "bg-control text-label shadow-sm" : "text-secondary"}`}
              >
                {label}
              </button>
            ))}
          </fieldset>,
          note("speed"),
        )}
        {row(
          "Copied in",
          draft.manifest.length === 0 ? (
            <span className="text-sm text-secondary">No agent config found on this Mac.</span>
          ) : (
            <div className="flex flex-col gap-1">
              {(st.draft.manifest.length ? st.draft.manifest : draft.manifest).map((m) => (
                <label key={m} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={draft.manifest.includes(m)}
                    onChange={(e) =>
                      update({
                        manifest: e.target.checked
                          ? [...draft.manifest, m]
                          : draft.manifest.filter((x) => x !== m),
                      })
                    }
                    className="accent-[var(--accent)]"
                  />
                  <span className="font-mono text-[12px]">~/{m}</span>
                </label>
              ))}
            </div>
          ),
          "Your agents' config, copied into every session so they start as you have them.",
        )}
        {st.agents.length > 0 &&
          row(
            "Agent skill",
            <div className="flex flex-col gap-1">
              {st.agents.map((a) => (
                <label key={a} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={skills.includes(a)}
                    onChange={(e) =>
                      setSkills(e.target.checked ? [...skills, a] : skills.filter((x) => x !== a))
                    }
                    className="accent-[var(--accent)]"
                  />
                  Install for {a.replace(/^\./, "")}
                </label>
              ))}
            </div>,
            "pier-onboard teaches your agent to set a repository up for pier.",
          )}
        {st.needsClaudeToken &&
          row(
            "Claude token",
            <div className="flex items-center gap-2">
              <input
                type="password"
                aria-label="Claude token"
                value={draft.claudeToken ?? ""}
                onChange={(e) => update({ claudeToken: e.target.value })}
                placeholder={st.hasClaudeToken ? "saved — paste to replace" : "paste the token"}
                className="selectable h-7 w-64 rounded-ctl border border-separator bg-control px-2 font-mono text-sm outline-none focus:border-accent"
              />
              <Button size="sm" icon={<Terminal size={11} />} onPress={() => setTerminal("claude-token")}>
                Get one…
              </Button>
            </div>,
            "Claude's login lives in this Mac's Keychain and can't travel; sessions sign in with a token instead.",
          )}
      </div>
      <Nav back={back} next={next} />
      <LocalTerminalSheet
        purpose={terminal}
        title="Get a Claude token"
        hint="Follow the steps; pier picks up the token it prints."
        onClose={() => setTerminal(undefined)}
        onCaptured={onTokenCaptured}
      />
    </section>
  );
}
