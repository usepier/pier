import { ExternalLink, Play, RotateCw, Square } from "lucide-react";
import { useEffect } from "react";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { Session } from "@/api/protocol.gen";
import { bytes } from "@/lib/format";
import { readable } from "@/lib/session";
import { recordLoad, useLoadHistory } from "@/state/history";
import { openPort, useProxy } from "@/state/proxy";
import { attempt } from "@/state/toasts";
import { Badge } from "@/ui/Badge";
import { IconButton } from "@/ui/Button";
import { Meter } from "@/ui/Panel";
import { Sparkline } from "@/ui/Sparkline";
import { Spinner } from "@/ui/Spinner";

function Section({ title, children, count }: { title: string; count?: number; children: React.ReactNode }) {
  return (
    <section className="border-b border-separator px-3 py-2.5">
      <h3 className="mb-1.5 flex items-center gap-1.5 text-xs font-semibold text-secondary">
        {title}
        {count !== undefined && <span className="font-normal text-tertiary">{count}</span>}
      </h3>
      {children}
    </section>
  );
}

/** What runs in a session: load, ports, containers, windows, the heaviest processes. */
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
  const hist = useLoadHistory(s.id);
  useEffect(() => recordLoad(s.id, env.data?.resources), [env.data, s.id]);
  if (!live) {
    return (
      <div className="p-4 text-sm text-secondary">
        {s.state === "parked" ? "Parked — nothing runs until it wakes." : "Not running."}
      </div>
    );
  }
  if (env.error) return <div className="selectable p-4 text-sm text-bad">{(env.error as Error).message}</div>;
  const e = env.data;
  if (!e) {
    return (
      <div className="flex items-center gap-2 p-4 text-sm text-secondary">
        <Spinner /> connecting…
      </div>
    );
  }
  const r = e.resources ?? s.resources;
  return (
    <div className="h-full overflow-y-auto text-sm">
      {r && (
        <Section title="Load">
          <div className="grid grid-cols-[3.5rem_1fr_auto] items-center gap-x-2 gap-y-1.5 text-xs">
            <span className="text-secondary">CPU</span>
            <Sparkline values={hist.cpu} max={100} width={120} />
            <span className="tabular-nums">{r.cpu.toFixed(0)}%</span>
            <span className="text-secondary">Memory</span>
            <Sparkline values={hist.mem} max={100} width={120} className="text-info" />
            <span className="tabular-nums">
              {bytes(r.memUsed)} / {bytes(r.memTotal)}
            </span>
            <span className="text-secondary">Disk</span>
            <Meter
              value={r.diskUsed}
              max={r.diskTotal}
              tone={r.diskUsed / r.diskTotal > 0.85 ? "warn" : "accent"}
            />
            <span className="tabular-nums">
              {bytes(r.diskUsed)} / {bytes(r.diskTotal)}
            </span>
          </div>
        </Section>
      )}
      <Section title="Ports" count={e.ports.length}>
        {e.ports.length === 0 ? (
          <div className="text-xs text-tertiary">Nothing listening.</div>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {e.ports.map((p) => (
              <li key={p.port} className="flex items-center gap-2">
                <span className="w-14 font-mono tabular-nums">{p.port}</span>
                <span className="min-w-0 flex-1 truncate text-secondary">{p.process}</span>
                {served?.ports.includes(p.port) && (
                  <span className="shrink-0 font-mono text-[10.5px] text-tertiary">{served.host}</span>
                )}
                <IconButton
                  label={`Open port ${p.port}`}
                  className="size-6"
                  onPress={() => void openPort(s, p.port)}
                >
                  <ExternalLink size={12} />
                </IconButton>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section title="Containers" count={e.containers.length}>
        {e.containers.length === 0 ? (
          <div className="text-xs text-tertiary">No containers.</div>
        ) : (
          <ul className="flex flex-col gap-1">
            {e.containers.map((c) => {
              const act = (action: string) =>
                attempt(() => rpc.call("containers.action", { sessionId: s.id, container: c.id, action }));
              return (
                <li key={c.id} className="flex items-center gap-2">
                  <span
                    className={`size-1.5 shrink-0 rounded-full ${c.state === "running" ? "bg-ok" : "bg-quaternary"}`}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="truncate">{c.name}</div>
                    <div className="truncate text-xs text-tertiary">
                      {c.image} · {c.status}
                    </div>
                  </div>
                  {c.state === "running" ? (
                    <>
                      <IconButton
                        label={`Restart ${c.name}`}
                        className="size-6"
                        onPress={() => void act("restart")}
                      >
                        <RotateCw size={12} />
                      </IconButton>
                      <IconButton
                        label={`Stop ${c.name}`}
                        className="size-6"
                        onPress={() => void act("stop")}
                      >
                        <Square size={11} />
                      </IconButton>
                    </>
                  ) : (
                    <IconButton
                      label={`Start ${c.name}`}
                      className="size-6"
                      onPress={() => void act("start")}
                    >
                      <Play size={12} />
                    </IconButton>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </Section>
      <Section title="Windows" count={e.windows.length}>
        <ul className="flex flex-col">
          {e.windows.map((w) => (
            <li key={w.id}>
              <button
                type="button"
                onClick={() => onOpenWindow(w.id)}
                className="flex w-full items-center gap-2 rounded px-1 py-0.5 text-left hover:bg-hover"
              >
                <span className="min-w-0 flex-1 truncate">{w.name}</span>
                {w.role && <Badge tone="accent">{w.role}</Badge>}
                <span className="truncate font-mono text-xs text-tertiary">{w.command}</span>
              </button>
            </li>
          ))}
        </ul>
      </Section>
      <Section title="Processes">
        <table className="w-full text-xs">
          <tbody>
            {e.processes.slice(0, 10).map((p) => (
              <tr key={p.pid}>
                <td className="max-w-0 truncate py-0.5 pr-2 font-mono" title={p.command}>
                  {p.command || `pid ${p.pid}`}
                </td>
                <td className="w-12 py-0.5 text-right tabular-nums">{p.cpu.toFixed(0)}%</td>
                <td className="w-16 py-0.5 text-right tabular-nums text-secondary">{bytes(p.rss)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
    </div>
  );
}
