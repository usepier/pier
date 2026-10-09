import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  Bot,
  Check,
  CircleAlert,
  Cloud,
  Copy,
  Download,
  Gauge,
  Globe,
  KeyRound,
  MessageSquare,
  MessageSquareText,
  Moon,
  Palette,
  Rocket,
  SquareTerminal,
  Stethoscope,
  Terminal,
} from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { Settings } from "@/api/protocol.gen";
import { confirm } from "@/lib/confirm";
import { ago } from "@/lib/format";
import { applyAccent } from "@/lib/theme";
import { trackJob } from "@/state/jobs";
import { useOverview } from "@/state/overview";
import { usePrefs } from "@/state/prefs";
import { startProxy, stopProxy, useProxy } from "@/state/proxy";
import { attempt, toast, toastError } from "@/state/toasts";
import { updatesAvailable, useUpdates } from "@/state/updates";
import { Button } from "@/ui/Button";
import { Kbd } from "@/ui/Kbd";
import { Select } from "@/ui/Select";
import { Spinner } from "@/ui/Spinner";
import { Switch } from "@/ui/Switch";
import type { Item, Section } from "./model";
import { codeSpans, pierItem, type Save } from "./PierSetting";
import { RepoPrompts, usePromptItems } from "./PromptItems";
import { TeardownSheet } from "./TeardownSheet";

const icon = (I: typeof Bot) => <I size={15} strokeWidth={1.75} />;

const minutes = [
  { value: "0", label: "Right away" },
  { value: "60000", label: "After 1 minute" },
  { value: "300000", label: "After 5 minutes" },
  { value: "600000", label: "After 10 minutes" },
  { value: "1800000", label: "After 30 minutes" },
];

/** Settings of the app itself, kept on this Mac. */
function useAppSections(): Section[] {
  const p = usePrefs();
  return [
    {
      id: "general",
      title: "General",
      group: "This app",
      icon: icon(MessageSquare),
      items: [
        {
          id: "send",
          title: "Send messages with",
          description: "Which key sends a message from the composer.",
          hint:
            p.sendKey === "enter" ? (
              <>
                Use <Kbd>⇧</Kbd>
                <Kbd>⏎</Kbd> for new lines
              </>
            ) : (
              <>
                Use <Kbd>⏎</Kbd> for new lines
              </>
            ),
          text: "enter return send key composer",
          control: (
            <Select
              label="Send messages with"
              width={180}
              value={p.sendKey}
              onChange={(v) => p.set({ sendKey: v })}
              options={[
                { value: "enter", label: "Enter" },
                { value: "cmdEnter", label: "⌘ Enter" },
              ]}
            />
          ),
        },
        {
          id: "followUp",
          title: "Follow-up behavior",
          description:
            "A message sent while the agent works steers its turn now, or waits in the queue for the turn to end.",
          hint: (
            <>
              Use <Kbd>⌘</Kbd>
              <Kbd>⇧</Kbd>
              <Kbd>⏎</Kbd> for the other
            </>
          ),
          text: "steer queue mid-turn follow up",
          control: (
            <Select
              label="Follow-up behavior"
              width={180}
              value={p.followUp}
              onChange={(v) => p.set({ followUp: v })}
              options={[
                { value: "steer", label: "Steer" },
                { value: "queue", label: "Queue" },
              ]}
            />
          ),
        },
        {
          id: "expandTools",
          title: "Show every tool call",
          description:
            "Keep each step the agent takes in view, instead of folding a run of them into one line.",
          text: "tools steps collapse fold expand",
          control: (
            <Switch
              label="Show every tool call"
              isSelected={p.expandTools}
              onChange={(v) => p.set({ expandTools: v })}
            />
          ),
        },
        {
          id: "startWithChat",
          title: "Start new sessions with a chat",
          description:
            "New Session opens on a chat with your default agent; its first message goes in once the session is set up.",
          text: "new session agent chat prompt",
          control: (
            <Switch
              label="Start new sessions with a chat"
              isSelected={p.startWithChat}
              onChange={(v) => p.set({ startWithChat: v })}
            />
          ),
        },
        {
          id: "autoRemove",
          title: "Remove merged sessions",
          description:
            "Once a session's pull request merges — only when everything in it is committed and merged.",
          text: "cleanup merged pull request remove",
          control: (
            <Switch
              label="Remove merged sessions"
              isSelected={p.autoRemoveMerged}
              onChange={(v) => p.set({ autoRemoveMerged: v })}
            />
          ),
        },
      ],
    },
    {
      id: "terminals",
      title: "Terminals",
      group: "This app",
      icon: icon(SquareTerminal),
      description:
        "A terminal tab attached to a session keeps it awake; these let sessions park when you look away.",
      items: [
        {
          id: "hiddenDetach",
          title: "Detach a hidden terminal",
          description: "A terminal tab you aren't looking at lets go of its session after this long.",
          text: "terminal detach hidden park",
          control: (
            <Select
              label="Detach a hidden terminal"
              width={200}
              value={String(p.hiddenDetachMs)}
              onChange={(v) => p.set({ hiddenDetachMs: Number(v) })}
              options={minutes}
            />
          ),
        },
        {
          id: "backgroundDetach",
          title: "Detach every terminal in the background",
          description:
            "Once the app has been out of sight this long, every terminal lets go and sessions can park.",
          text: "terminal detach background park",
          control: (
            <Select
              label="Detach every terminal in the background"
              width={200}
              value={String(p.backgroundDetachMs)}
              onChange={(v) => p.set({ backgroundDetachMs: Number(v) })}
              options={minutes}
            />
          ),
        },
        {
          id: "fontSize",
          title: "Font size",
          description: "The terminals' text size.",
          text: "terminal font size",
          control: (
            <Select
              label="Terminal font size"
              width={120}
              value={String(p.terminalFontSize)}
              onChange={(v) => p.set({ terminalFontSize: Number(v) })}
              options={[11, 12, 12.5, 13, 14, 15, 16].map((n) => ({ value: String(n), label: `${n} pt` }))}
            />
          ),
        },
      ],
    },
  ];
}

const pierSections: { group: string; id: string; title: string; icon: typeof Bot; description?: string }[] = [
  {
    group: "agents",
    id: "agents",
    title: "Agents",
    icon: Bot,
    description:
      "How new chats start — in the app, the TUI and `pier <branch> -p`. A chat can change its model, effort and permissions as it goes.",
  },
  { group: "cloud", id: "cloud", title: "Cloud", icon: Cloud },
  { group: "sessions", id: "sessions", title: "New sessions", icon: Rocket },
  {
    group: "idle",
    id: "idle",
    title: "Idle & cost",
    icon: Moon,
    description: "Sessions park themselves: the VM stops and only its disk costs money.",
  },
  {
    group: "speed",
    id: "speed",
    title: "Speed & pools",
    icon: Gauge,
    description: "Session images and pools of parked sessions make new sessions start in seconds.",
  },
  { group: "app", id: "appearance", title: "Appearance", icon: Palette },
];

/** pier's own settings (shared with the CLI and TUI), by group. */
function usePierSections(settings: Settings | undefined, save: Save): Section[] {
  if (!settings) return [];
  return pierSections
    .map((d) => ({
      id: d.id,
      title: d.title,
      group: d.group === "app" ? ("This app" as const) : ("pier" as const),
      icon: icon(d.icon),
      description: d.description ? codeSpans(d.description) : undefined,
      items: settings.settings.filter((s) => s.group === d.group).map((s) => pierItem(s, save)),
    }))
    .filter((s) => s.items.length > 0);
}

/** One section per linked repository. */
function useRepoSections(): Section[] {
  const o = useOverview();
  const qc = useQueryClient();
  const navigate = useNavigate();
  return (o?.repos ?? [])
    .filter((r) => r.checkout && !r.checkout.missing)
    .map((r) => {
      const path = r.checkout!.path;
      const items: Item[] = [
        {
          id: `${r.name}.checkout`,
          title: "Linked clone",
          description: <span className="selectable font-mono text-sm">{path}</span>,
          text: `clone checkout link ${path}`,
          control: (
            <Button
              size="sm"
              variant="plain"
              onPress={async () => {
                if (
                  !(await confirm(
                    `Unlink ${r.name}?`,
                    "Its sessions keep running; new ones need a linked clone.",
                    { ok: "Unlink" },
                  ))
                )
                  return;
                await attempt(() => rpc.call("repos.unlink", { path }));
                void qc.invalidateQueries();
              }}
            >
              Unlink…
            </Button>
          ),
        },
        {
          id: `${r.name}.pool`,
          title: "Pool size",
          description:
            "Parked sessions kept ready, so a new one starts in seconds. Each costs its disk while it waits.",
          hint: `${r.poolReady} ready${r.poolFilling ? `, ${r.poolFilling} filling` : ""}${r.poolStale ? `, ${r.poolStale} stale` : ""}`,
          text: "pool ready warm sessions size",
          control: (
            <Select
              label={`${r.name} pool size`}
              width={120}
              value={String(r.poolTarget)}
              onChange={(v) => void attempt(() => rpc.call("pool.setSize", { path, size: Number(v) }))}
              options={[0, 1, 2, 3, 4, 5].map((n) => ({
                value: String(n),
                label: n === 0 ? "None" : String(n),
              }))}
            />
          ),
        },
        {
          id: `${r.name}.image`,
          title: "Session image",
          description: r.image
            ? `${r.imageKind === "prebuilt" ? "Toolchains and the repo, prebuilt" : "Toolchains"} — baked ${r.bakedAt ? ago(r.bakedAt) : "earlier"}.`
            : "None yet: new sessions run the repo's whole setup. A bake makes them start in a minute or two.",
          text: "image bake",
          control: (
            <Button
              size="sm"
              isDisabled={r.baking}
              onPress={async () => {
                const job = await attempt(() => rpc.call("image.bake", { path }));
                if (job) {
                  trackJob(job);
                  toast(`Baking ${r.name}'s image`, "info");
                }
              }}
            >
              {r.baking ? "Baking…" : r.image ? "Bake again" : "Bake"}
            </Button>
          ),
        },
        {
          id: `${r.name}.prompts`,
          title: "Prompts",
          description:
            "What pier asks the agent in this repo's sessions. Empty ones use your prompts for every repo.",
          text: "prompt wording fix ci address review comments create pull request onboard",
          children: <RepoPrompts repo={r.name} />,
        },
        {
          id: `${r.name}.cockpit`,
          title: "Sessions, cost and pull requests",
          description: "The repository's overview.",
          control: (
            <Button
              size="sm"
              variant="plain"
              onPress={() => navigate({ to: "/repo/$name", params: { name: r.name } })}
            >
              Open
            </Button>
          ),
        },
      ];
      return {
        id: `repo:${r.name}`,
        title: r.name,
        group: "Repositories" as const,
        icon: (
          <span className="flex size-[15px] items-center justify-center rounded-[4px] bg-selected text-[9px] font-semibold uppercase text-secondary">
            {r.name[0]}
          </span>
        ),
        description: r.origin,
        items,
      };
    });
}

/** Local names, checks, the command line, the account, updates. */
function useMoreSections(): { sections: Section[]; sheet: React.ReactNode } {
  const proxy = useProxy();
  const prefs = usePrefs();
  const [busy, setBusy] = useState(false);
  const checks = useQuery({
    queryKey: ["doctor"],
    queryFn: () => rpc.call("doctor.run", {}),
    enabled: false,
  });
  const cli = useQuery({ queryKey: ["cli"], queryFn: () => rpc.call("cli.status", {}) });
  const navigate = useNavigate();
  const [tearingDown, setTearingDown] = useState(false);
  const updates = useUpdates();
  const toggle = async () => {
    setBusy(true);
    if (proxy?.running) await stopProxy();
    else await startProxy();
    setBusy(false);
  };
  const sections: Section[] = [
    {
      id: "names",
      title: "Local names",
      group: "More",
      icon: icon(Globe),
      description:
        "Every running session answers at <session>.pier, its ports mirrored on this Mac — localhost too.",
      items: [
        {
          id: "proxy",
          title: proxy?.running
            ? `On — ${proxy.sessions.length} running session${proxy.sessions.length === 1 ? "" : "s"}`
            : "Off",
          description:
            proxy?.error ||
            "It runs in the background, shared with the pier command, and keeps running after pier quits. The first start asks for an administrator's password, once.",
          text: "proxy names dns ports pier",
          control: (
            <Button
              size="sm"
              variant={proxy?.running ? "secondary" : "primary"}
              isDisabled={busy}
              onPress={() => void toggle()}
            >
              {busy ? <Spinner size={11} /> : proxy?.running ? "Turn off" : "Turn on"}
            </Button>
          ),
        },
        ...(proxy?.running && !proxy.caTrusted
          ? [
              {
                id: "https",
                title: "https",
                description:
                  "Browsers warn on https://<session>.pier until this Mac trusts pier's local certificate.",
                control: (
                  <Button
                    size="sm"
                    onPress={async () => {
                      if ((await attempt(() => rpc.call("proxy.trustCA", {}))) !== undefined)
                        toast("Trusted", "ok");
                    }}
                  >
                    Trust…
                  </Button>
                ),
              },
            ]
          : []),
        {
          id: "proxyForget",
          title: "Remove the local setup",
          description:
            "Stops the proxy and takes its resolver, boot task and loopback addresses off this Mac. Turning it on again sets them up again.",
          text: "proxy remove forget resolver uninstall",
          control: (
            <Button
              size="sm"
              variant="plain"
              onPress={async () => {
                if (
                  !(await confirm(
                    "Remove the local setup?",
                    "Sessions stop answering at <session>.pier until the proxy is turned on again.",
                    { ok: "Remove" },
                  ))
                )
                  return;
                if ((await attempt(() => rpc.call("proxy.forget", {}))) !== undefined) toast("Removed", "ok");
              }}
            >
              Remove…
            </Button>
          ),
        },
        {
          id: "proxyOnLaunch",
          title: "Turn on when pier opens",
          text: "proxy launch start",
          control: (
            <Switch
              label="Turn on when pier opens"
              isSelected={prefs.proxyOnLaunch}
              onChange={(v) => prefs.set({ proxyOnLaunch: v })}
            />
          ),
        },
      ],
    },
    {
      id: "checks",
      title: "Checks",
      group: "More",
      icon: icon(Stethoscope),
      items: [
        {
          id: "doctor",
          title: "Environment and account",
          description: "The tools pier needs on this Mac, the cloud sign-in, and the account's groundwork.",
          text: "doctor checks",
          control: (
            <Button size="sm" isDisabled={checks.isFetching} onPress={() => void checks.refetch()}>
              {checks.isFetching ? "Checking…" : checks.data ? "Check again" : "Run checks"}
            </Button>
          ),
          children:
            checks.error || checks.data ? (
              <div className="flex flex-col divide-y divide-separator rounded-card border border-separator">
                {checks.error && (
                  <div className="selectable px-3 py-2 text-sm text-bad">
                    {(checks.error as Error).message}
                  </div>
                )}
                {checks.data?.map((c) => (
                  <div key={c.name} className="flex items-center gap-2 px-3 py-2 text-sm">
                    {c.status === "ok" ? (
                      <Check size={13} className="shrink-0 text-ok" />
                    ) : (
                      <CircleAlert size={13} className="shrink-0 text-warn" />
                    )}
                    <span className="shrink-0">{c.name}</span>
                    <span className="selectable min-w-0 flex-1 truncate text-secondary" title={c.detail}>
                      {c.detail}
                    </span>
                    {c.fix && c.status !== "ok" && (
                      <button
                        type="button"
                        title={`Copy: ${c.fix}`}
                        onClick={() => {
                          void navigator.clipboard.writeText(c.fix!);
                          toast("Copied", "ok");
                        }}
                        className="inline-flex shrink-0 items-center gap-1 rounded-[5px] border border-separator px-1.5 py-0.5 font-mono text-[11px] transition-colors hover:bg-hover"
                      >
                        {c.fix} <Copy size={10} />
                      </button>
                    )}
                  </div>
                ))}
              </div>
            ) : undefined,
        },
      ],
    },
    {
      id: "cli",
      title: "Command line",
      group: "More",
      icon: icon(Terminal),
      items: [
        {
          id: "cli.link",
          title: "The pier command",
          description: cli.data?.provided
            ? `Your terminal's pier is this app's — Homebrew put it at ${cli.data.provided}.`
            : cli.data?.stale
              ? `${cli.data.link} points at a copy of the app that moved or is gone.`
              : cli.data?.installed
                ? `Installed at ${cli.data.link}${cli.data.other ? ` — but your shell finds ${cli.data.other} first` : ""}.`
                : `Puts this app's pier at ${cli.data?.link ?? "/usr/local/bin/pier"}, for your terminal.`,
          text: "cli command install terminal repair",
          control:
            cli.data && !cli.data.installed && !cli.data.provided ? (
              <Button
                size="sm"
                onPress={async () => {
                  const r = await attempt(() => rpc.call("cli.install", {}));
                  if (r?.installed) toast(cli.data?.stale ? "Repaired" : "Installed", "ok");
                  void cli.refetch();
                }}
              >
                {cli.data.stale ? "Repair…" : "Install…"}
              </Button>
            ) : undefined,
        },
      ],
    },
    {
      id: "account",
      title: "Account",
      group: "More",
      icon: icon(KeyRound),
      items: [
        {
          id: "setup",
          title: "Setup",
          description: "Change the cloud or account, or redo the defaults.",
          text: "setup cloud account",
          control: (
            <Button size="sm" onPress={() => navigate({ to: "/setup" })}>
              Set up again…
            </Button>
          ),
        },
        {
          id: "teardown",
          title: "Remove pier from the account",
          description: "Deletes the pools, images and groundwork. Sessions must be removed first.",
          text: "teardown remove account",
          control: (
            <Button
              size="sm"
              variant="danger"
              onPress={async () => {
                if (
                  await confirm(
                    "Remove pier from the account?",
                    "Pooled sessions, session images and the account's groundwork go.",
                    { ok: "Continue", destructive: true },
                  )
                )
                  setTearingDown(true);
              }}
            >
              Remove…
            </Button>
          ),
        },
      ],
    },
    {
      id: "updates",
      title: "Updates",
      group: "More",
      icon: icon(Download),
      items: [
        {
          id: "version",
          title: `pier ${rpc.info?.version ?? ""}`,
          description: updates.update
            ? `pier ${updates.update.version} is out.`
            : updates.error
              ? updates.error
              : updates.checkedAt
                ? `Up to date — checked ${ago(new Date(updates.checkedAt).toISOString())}.`
                : "pier looks for updates every few hours.",
          text: "update version check",
          control: updates.update ? (
            <Button
              size="sm"
              variant="primary"
              isDisabled={updates.progress !== undefined}
              onPress={() => void updates.install().catch(toastError)}
            >
              {updates.progress === undefined
                ? "Restart to update"
                : `Updating… ${Math.round(updates.progress * 100)}%`}
            </Button>
          ) : (
            <Button
              size="sm"
              isDisabled={updates.checking || !updatesAvailable}
              onPress={() => void updates.check()}
            >
              {updates.checking ? "Checking…" : "Check now"}
            </Button>
          ),
        },
      ],
    },
  ];
  return {
    sections,
    sheet: <TeardownSheet open={tearingDown} onClose={() => setTearingDown(false)} />,
  };
}

/** Every section of the settings page, in nav order, and what they show besides. */
export function useSections(): {
  sections: Section[];
  loading: boolean;
  error?: unknown;
  extra: React.ReactNode;
} {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["settings"], queryFn: () => rpc.call("settings.get", {}) });
  const save: Save = async (key, value) => {
    const next = await attempt(() => rpc.call("settings.set", { key, value }));
    if (!next) return;
    qc.setQueryData(["settings"], next);
    if (key === "theme") applyAccent(value);
    toast("Saved", "ok");
  };
  const app = useAppSections();
  const pier = usePierSections(q.data, save);
  const repos = useRepoSections();
  const more = useMoreSections();
  const promptItems = usePromptItems("");
  const prompts: Section[] = promptItems.length
    ? [
        {
          id: "prompts",
          title: "Prompts",
          group: "pier",
          icon: icon(MessageSquareText),
          description: "What pier asks the agent for you. A repository can have its own.",
          items: promptItems,
        },
      ]
    : [];
  const appearance = pier.filter((s) => s.group === "This app");
  return {
    sections: [
      ...app,
      ...appearance,
      ...pier.filter((s) => s.group === "pier"),
      ...prompts,
      ...repos,
      ...more.sections,
    ],
    loading: q.isLoading,
    error: q.error,
    extra: more.sheet,
  };
}
