import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Check, CircleAlert, Copy } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { LocalPurpose } from "@/api/protocol.gen";
import { LocalTerminalSheet } from "@/features/setup/LocalTerminalSheet";
import { confirm } from "@/lib/confirm";
import { usePrefs } from "@/state/prefs";
import { startProxy, stopProxy, useProxy } from "@/state/proxy";
import { attempt, toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="mb-2 px-1 text-xs font-semibold text-secondary">{title}</h2>
      <div className="divide-y divide-separator rounded-card border border-separator bg-raised/60">
        {children}
      </div>
    </section>
  );
}

function Item({
  title,
  detail,
  children,
}: {
  title: string;
  detail?: React.ReactNode;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-4 px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="text-base">{title}</div>
        {detail && <div className="text-xs text-secondary">{detail}</div>}
      </div>
      {children && <div className="flex shrink-0 items-center gap-2">{children}</div>}
    </div>
  );
}

/** Sessions as <session>.pier on this Mac. */
export function ProxySection() {
  const proxy = useProxy();
  const prefs = usePrefs();
  const [busy, setBusy] = useState(false);
  const toggle = async () => {
    setBusy(true);
    if (proxy?.running) await stopProxy();
    else await startProxy();
    setBusy(false);
  };
  return (
    <Section title="Local names">
      <Item
        title={
          proxy?.running
            ? `On — ${proxy.sessions.length} running session${proxy.sessions.length === 1 ? "" : "s"}`
            : "Off"
        }
        detail={
          proxy?.error ||
          "Every running session answers at <session>.pier, its ports mirrored on this Mac — localhost too. The first start asks for an administrator's password."
        }
      >
        <Button
          size="sm"
          variant={proxy?.running ? "secondary" : "primary"}
          isDisabled={busy}
          onPress={() => void toggle()}
        >
          {busy ? <Spinner size={11} /> : proxy?.running ? "Turn off" : "Turn on"}
        </Button>
      </Item>
      {proxy?.running && !proxy.caTrusted && (
        <Item
          title="https"
          detail="Browsers warn on https://<session>.pier until this Mac trusts pier's local certificate."
        >
          <Button
            size="sm"
            onPress={async () => {
              if ((await attempt(() => rpc.call("proxy.trustCA", {}))) !== undefined) toast("Trusted", "ok");
            }}
          >
            Trust…
          </Button>
        </Item>
      )}
      <Item title="Turn on when pier opens">
        <input
          type="checkbox"
          aria-label="Turn on when pier opens"
          checked={prefs.proxyOnLaunch}
          onChange={(e) => prefs.set({ proxyOnLaunch: e.target.checked })}
          className="size-4 accent-[var(--accent)]"
        />
      </Item>
    </Section>
  );
}

/** The checks pier doctor runs, each with its fix. */
export function ChecksSection() {
  const checks = useQuery({
    queryKey: ["doctor"],
    queryFn: () => rpc.call("doctor.run", {}),
    enabled: false,
  });
  return (
    <Section title="Checks">
      <Item
        title="Environment and account"
        detail="The tools pier needs here, the cloud sign-in, and the account's groundwork."
      >
        <Button size="sm" isDisabled={checks.isFetching} onPress={() => void checks.refetch()}>
          {checks.isFetching ? "Checking…" : checks.data ? "Check again" : "Run checks"}
        </Button>
      </Item>
      {checks.error && (
        <div className="selectable px-3 py-2 text-sm text-bad">{(checks.error as Error).message}</div>
      )}
      {checks.data?.map((c) => (
        <div key={c.name} className="flex items-center gap-2 px-3 py-1.5 text-sm">
          {c.status === "ok" ? (
            <Check size={13} className="shrink-0 text-ok" />
          ) : (
            <CircleAlert size={13} className="shrink-0 text-warn" />
          )}
          <span className="shrink-0">{c.name}</span>
          <span className="selectable min-w-0 flex-1 truncate text-xs text-secondary" title={c.detail}>
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
              className="inline-flex shrink-0 items-center gap-1 rounded bg-selected px-1.5 py-0.5 font-mono text-[11px]"
            >
              {c.fix} <Copy size={10} />
            </button>
          )}
        </div>
      ))}
    </Section>
  );
}

/** The pier command in the shell, setup again, and taking pier out of the account. */
export function AccountSection() {
  const navigate = useNavigate();
  const cli = useQuery({ queryKey: ["cli"], queryFn: () => rpc.call("cli.status", {}) });
  const [terminal, setTerminal] = useState<LocalPurpose>();
  return (
    <Section title="This Mac and the account">
      <Item
        title="The pier command"
        detail={
          cli.data?.installed
            ? `Installed at ${cli.data.link}${cli.data.other ? ` — but your shell finds ${cli.data.other} first` : ""}.`
            : `Puts this app's pier at ${cli.data?.link ?? "/usr/local/bin/pier"}, for your terminal.`
        }
      >
        {cli.data && !cli.data.installed && (
          <Button
            size="sm"
            onPress={async () => {
              const r = await attempt(() => rpc.call("cli.install", {}));
              if (r?.installed) toast("Installed", "ok");
              void cli.refetch();
            }}
          >
            Install…
          </Button>
        )}
      </Item>
      <Item title="Setup" detail="Change the cloud or account, or redo the defaults.">
        <Button size="sm" onPress={() => navigate({ to: "/setup" })}>
          Set up again…
        </Button>
      </Item>
      <Item
        title="Remove pier from the account"
        detail="Deletes the pool, images and groundwork. Sessions must be removed first."
      >
        <Button
          size="sm"
          variant="danger"
          onPress={async () => {
            if (
              await confirm(
                "Remove pier from the account?",
                "Pooled sessions, session images and the account's groundwork go. pier asks once more in the terminal.",
                { ok: "Continue", destructive: true },
              )
            )
              setTerminal("teardown");
          }}
        >
          Remove…
        </Button>
      </Item>
      <LocalTerminalSheet
        purpose={terminal}
        title="Remove pier from the account"
        onClose={() => setTerminal(undefined)}
      />
    </Section>
  );
}
