// Local names, checks, the command line, the account, updates.
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Check, CircleAlert, Copy, Download, Globe, KeyRound, Stethoscope, Terminal } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import { confirm } from "@/lib/confirm";
import { ago } from "@/lib/format";
import { usePrefs } from "@/state/prefs";
import { startProxy, stopProxy, useProxy } from "@/state/proxy";
import { attempt, toast, toastError } from "@/state/toasts";
import { updatesAvailable, useUpdates } from "@/state/updates";
import { ActionButton, Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";
import { Switch } from "@/ui/Switch";
import { icon } from "./icon";
import type { Section } from "./model";
import { TeardownSheet } from "./TeardownSheet";

/** Local names, checks, the command line, the account, updates. */
export function useMoreSections(): { sections: Section[]; sheet: React.ReactNode } {
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
                  <ActionButton
                    size="sm"
                    action={async () => {
                      if ((await attempt(() => rpc.call("proxy.trustCA", {}))) !== undefined)
                        toast("Trusted", "ok");
                    }}
                  >
                    Trust…
                  </ActionButton>
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
            <ActionButton
              size="sm"
              variant="plain"
              action={async () => {
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
            </ActionButton>
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
              <ActionButton
                size="sm"
                action={async () => {
                  const r = await attempt(() => rpc.call("cli.install", {}));
                  if (r?.installed) toast(cli.data?.stale ? "Repaired" : "Installed", "ok");
                  void cli.refetch();
                }}
              >
                {cli.data.stale ? "Repair…" : "Install…"}
              </ActionButton>
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
            <ActionButton
              size="sm"
              variant="danger"
              action={async () => {
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
            </ActionButton>
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
