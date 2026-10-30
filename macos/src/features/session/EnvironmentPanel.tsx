import { ExternalLink, Play, RotateCw, Square, SquareTerminal } from "lucide-react";
import { type ReactNode, useState } from "react";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { Container, Session } from "@/api/protocol.gen";
import { bytes } from "@/lib/format";
import { readable } from "@/lib/session";
import { LOAD_WINDOW_MS, useLoad } from "@/state/history";
import { openPort, useProxy } from "@/state/proxy";
import { attempt } from "@/state/toasts";
import { IconButton } from "@/ui/Button";
import { LoadGraph } from "@/ui/LoadGraph";
import { Meter } from "@/ui/Panel";
import { Nothing, rowClass, Section } from "@/ui/Section";
import { Spinner } from "@/ui/Spinner";
import { HoverTip } from "@/ui/Tooltip";

/** One reading of the machine: what it is now, and how it went. */
function Gauge({
  label,
  value,
  of,
  children,
}: {
  label: string;
  value: string;
  of?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-card border border-separator px-3 pt-2.5 pb-3">
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="text-xs text-secondary">{label}</span>
        <span className="truncate whitespace-nowrap">
          <span className="text-md font-medium tabular-nums">{value}</span>
          {of && <span className="text-xs text-tertiary tabular-nums"> / {of}</span>}
        </span>
      </div>
      {children}
    </div>
  );
}

const roleText: Record<string, string> = { home: "the session's own shell", setup: "where setup ran" };

/**
 * What runs in a session, in the order it's looked for: how the machine is
 * doing, what it serves (ports, containers), its terminals, and what's
 * busiest.
 */
export function EnvironmentPanel({
  session: s,
  onOpenWindow,
}: {
  session: Session;
  onOpenWindow: (id: string) => void;
}) {
  const proxy = useProxy();
  const served = proxy?.running ? proxy.sessions.find((x) => x.id === s.id) : undefined;
  const live = readable(s);
  const env = useWatch("env", { id: s.id }, { enabled: live });
  const load = useLoad(s);
  if (!live) {
    return (
      <div className="p-5 text-sm text-secondary">
        {s.state === "parked"
          ? "Parked — nothing runs until it wakes. Its load shows here again once it does."
          : "Not running."}
      </div>
    );
  }
  if (env.error) return <div className="selectable p-5 text-sm text-bad">{(env.error as Error).message}</div>;
  const e = env.data;
  if (!e) {
    return (
      <div className="flex items-center gap-2 p-5 text-sm text-secondary">
        <Spinner /> Connecting…
      </div>
    );
  }
  const r = load.now;
  return (
    <div className="h-full overflow-y-auto text-sm">
      {r && (
        <Section title="Machine">
          <div className="grid grid-cols-2 gap-2.5">
            <Gauge label="CPU" value={`${r.cpu.toFixed(0)}%`}>
              <LoadGraph
                label="CPU over the last 10 minutes"
                points={load.readings.map((x) => ({ t: x.t, v: x.cpu }))}
                window={LOAD_WINDOW_MS}
                min={10}
                max={100}
              />
            </Gauge>
            <Gauge label="Memory" value={bytes(r.memUsed)} of={bytes(r.memTotal)}>
              <LoadGraph
                label="Memory over the last 10 minutes"
                points={load.readings.map((x) => ({ t: x.t, v: x.mem }))}
                window={LOAD_WINDOW_MS}
                min={r.memTotal / 4}
                max={r.memTotal}
                className="text-info"
              />
            </Gauge>
            <div className="col-span-2">
              <Gauge label="Disk" value={bytes(r.diskUsed)} of={bytes(r.diskTotal)}>
                <Meter
                  value={r.diskUsed}
                  max={r.diskTotal}
                  tone={r.diskUsed / r.diskTotal > 0.85 ? "warn" : "accent"}
                />
              </Gauge>
            </div>
          </div>
        </Section>
      )}

      <Section title="Ports" count={e.ports.length}>
        {e.ports.length === 0 ? (
          <Nothing>Nothing is listening yet.</Nothing>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {e.ports.map((p) => (
              <li key={p.port}>
                <button
                  type="button"
                  aria-label={`Open port ${p.port}`}
                  onClick={() => void openPort(s, p.port, proxy)}
                  className={`${rowClass} group hover:bg-hover`}
                >
                  <span className="w-12 shrink-0 font-mono tabular-nums">{p.port}</span>
                  <span className="min-w-0 flex-1 truncate text-secondary">{p.process}</span>
                  {served?.ports.includes(p.port) && (
                    <span className="min-w-0 truncate font-mono text-xs text-tertiary">{served.host}</span>
                  )}
                  <ExternalLink size={13} className="shrink-0 text-tertiary group-hover:text-label" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="Containers" count={e.containers.length}>
        {e.containers.length === 0 ? (
          <Nothing>No containers.</Nothing>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {e.containers.map((c) => (
              <ContainerRow key={c.id} sessionId={s.id} c={c} />
            ))}
          </ul>
        )}
      </Section>

      <Section title="Terminals" count={e.windows.length}>
        <ul className="flex flex-col gap-0.5">
          {e.windows.map((w) => (
            <li key={w.id}>
              <button
                type="button"
                onClick={() => onOpenWindow(w.id)}
                title={w.role ? roleText[w.role] : undefined}
                className={`${rowClass} hover:bg-hover`}
              >
                <SquareTerminal size={14} className="shrink-0 text-tertiary" />
                <span className="min-w-0 flex-1 truncate">{w.name}</span>
                <span className="min-w-0 truncate font-mono text-xs text-tertiary">{w.command}</span>
              </button>
            </li>
          ))}
        </ul>
      </Section>

      {e.processes.length > 0 && (
        <Section title="Busiest processes">
          <ul className="flex flex-col">
            {e.processes.slice(0, 8).map((p) => (
              <li key={p.pid} className={rowClass} title={p.command}>
                <span className="min-w-0 flex-1 truncate font-mono text-xs">
                  {p.command || `pid ${p.pid}`}
                </span>
                <span className="w-10 shrink-0 text-right tabular-nums">{p.cpu.toFixed(0)}%</span>
                <span className="w-16 shrink-0 text-right text-secondary tabular-nums">{bytes(p.rss)}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  );
}

/** A container, and what can be done to it — each saying while it runs. */
function ContainerRow({ sessionId, c }: { sessionId: string; c: Container }) {
  const [doing, setDoing] = useState<string>();
  const act = async (action: string) => {
    if (doing) return;
    setDoing(action);
    await attempt(() => rpc.call("containers.action", { sessionId, container: c.id, action }));
    setDoing(undefined);
  };
  const up = c.state === "running";
  return (
    <li className={`${rowClass} group py-1.5 hover:bg-hover`}>
      <HoverTip content={up ? "Running" : c.state}>
        <span className={`size-2 shrink-0 rounded-full ${up ? "bg-ok" : "bg-quaternary"}`} />
      </HoverTip>
      <div className="min-w-0 flex-1">
        <div className="truncate">{c.name}</div>
        <div className="truncate text-xs text-tertiary">
          {doing ? `${doingText[doing] ?? doing}…` : `${c.image} · ${c.status}`}
        </div>
      </div>
      {up ? (
        <>
          <IconButton
            label={`Restart ${c.name}`}
            isPending={doing === "restart"}
            isDisabled={!!doing && doing !== "restart"}
            onPress={() => void act("restart")}
          >
            <RotateCw size={13} />
          </IconButton>
          <IconButton
            label={`Stop ${c.name}`}
            isPending={doing === "stop"}
            isDisabled={!!doing && doing !== "stop"}
            onPress={() => void act("stop")}
          >
            <Square size={12} />
          </IconButton>
        </>
      ) : (
        <IconButton label={`Start ${c.name}`} isPending={doing === "start"} onPress={() => void act("start")}>
          <Play size={13} />
        </IconButton>
      )}
    </li>
  );
}

const doingText: Record<string, string> = { restart: "Restarting", stop: "Stopping", start: "Starting" };
