import { Bot, Plus, SquareTerminal, Wrench, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { Session, Window } from "@/api/protocol.gen";
import { TerminalView } from "@/features/terminal/TerminalView";
import { confirm } from "@/lib/confirm";
import { attempt } from "@/state/toasts";
import { IconButton } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";

const shells = new Set(["bash", "zsh", "sh", "fish", "-bash", "-zsh"]);

function label(w: Window): string {
  if (w.role === "agent") return "Agent";
  if (w.role === "setup") return w.name === "setup-failed" ? "Setup failed" : "Setup";
  return w.name || w.command || w.id;
}

/** The session's tmux windows as tabs: the agent, the setup, and terminals of your own. */
export function SessionCenter({ session: s, openWindow }: { session: Session; openWindow?: string }) {
  const env = useWatch("env", { id: s.id });
  const windows = useMemo(() => [...(env.data?.windows ?? [])].sort((a, b) => a.index - b.index), [env.data]);
  const [active, setActive] = useState<string>();
  const [visited, setVisited] = useState<string[]>([]);

  // Start on the agent's window, else whatever tmux has current.
  useEffect(() => {
    if (active && windows.some((w) => w.id === active)) return;
    const pick = windows.find((w) => w.role === "agent") ?? windows.find((w) => w.active) ?? windows[0];
    if (pick) setActive(pick.id);
  }, [windows, active]);
  useEffect(() => {
    if (openWindow) setActive(openWindow);
  }, [openWindow]);
  useEffect(() => {
    if (active && !visited.includes(active)) setVisited((v) => [...v, active]);
  }, [active, visited]);

  const add = async () => {
    const w = await attempt(() => rpc.call("tmux.newWindow", { sessionId: s.id }));
    if (w) setActive(w.id);
  };
  const close = async (w: Window) => {
    const busy = w.command && !shells.has(w.command);
    if (
      busy &&
      !(await confirm(`Close ${label(w)}?`, `${w.command} is still running in it; closing ends it.`, {
        ok: "Close",
        destructive: true,
      }))
    )
      return;
    await attempt(() => rpc.call("tmux.killWindow", { sessionId: s.id, window: w.id }));
    setVisited((v) => v.filter((x) => x !== w.id));
  };

  if (!env.data) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-secondary">
        {env.error ? (
          <span className="selectable text-bad">{(env.error as Error).message}</span>
        ) : (
          <>
            <Spinner /> connecting…
          </>
        )}
      </div>
    );
  }
  return (
    <div className="flex h-full flex-col">
      <div role="tablist" className="flex h-9 shrink-0 items-center gap-0.5 border-b border-separator px-2">
        {windows.map((w) => {
          const on = w.id === active;
          const Icon = w.role === "agent" ? Bot : w.role === "setup" ? Wrench : SquareTerminal;
          return (
            <div
              key={w.id}
              className={`group flex h-7 items-center rounded-ctl ${on ? "bg-selected" : "hover:bg-hover"}`}
            >
              <button
                type="button"
                role="tab"
                aria-selected={on}
                onClick={() => setActive(w.id)}
                className={`flex items-center gap-1.5 pl-2.5 text-sm ${on ? "text-label" : "text-secondary"} ${w.role ? "pr-2.5" : "pr-1"}`}
              >
                <Icon size={13} className={w.name === "setup-failed" ? "text-bad" : ""} />
                <span className="max-w-40 truncate">{label(w)}</span>
              </button>
              {!w.role && (
                <IconButton
                  label={`Close ${label(w)}`}
                  className="mr-0.5 size-5 opacity-0 group-hover:opacity-100"
                  onPress={() => void close(w)}
                >
                  <X size={11} />
                </IconButton>
              )}
            </div>
          );
        })}
        <IconButton label="New terminal" className="size-7" onPress={() => void add()}>
          <Plus size={14} />
        </IconButton>
      </div>
      <div className="relative min-h-0 flex-1">
        {visited
          .filter((id) => windows.some((w) => w.id === id))
          .map((id) => (
            <div key={id} className="absolute inset-0">
              <TerminalView sessionId={s.id} window={id} visible={id === active} />
            </div>
          ))}
      </div>
    </div>
  );
}
