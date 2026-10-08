import { FileCode, GitCompareArrows, MessageSquare, Plus, SquareTerminal, Wrench, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { Session, Window } from "@/api/protocol.gen";
import { ChatView } from "@/features/chat/ChatView";
import { confirmClose, FileEditor } from "@/features/files/FileEditor";
import { DiffView } from "@/features/review/DiffView";
import { TerminalView } from "@/features/terminal/TerminalView";
import { confirm } from "@/lib/confirm";
import { bufferKey, useBuffers } from "@/state/buffers";
import { type Doc, useDocs } from "@/state/docs";
import { attempt } from "@/state/toasts";
import { IconButton } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";

const shells = new Set(["bash", "zsh", "sh", "fish", "-bash", "-zsh"]);

function label(w: Window): string {
  if (w.role === "agent") return "Agent";
  if (w.role === "setup") return w.name === "setup-failed" ? "Setup failed" : "Setup";
  return w.name || w.command || w.id;
}

const chatTab = "chat";
const noDocs: Doc[] = [];

function baseName(p: string) {
  return p.slice(p.lastIndexOf("/") + 1);
}

/**
 * The session's tabs: the agent's chat first (its own terminal one switch
 * away), the setup and terminals of your own (tmux windows), then the
 * diffs and files opened from the inspector.
 */
export function SessionCenter({ session: s, openWindow }: { session: Session; openWindow?: string }) {
  const env = useWatch("env", { id: s.id });
  const all = useMemo(() => [...(env.data?.windows ?? [])].sort((a, b) => a.index - b.index), [env.data]);
  const agentWindow = all.find((w) => w.role === "agent");
  const hasChat = !!agentWindow || !!s.agent;
  // The agent's window lives behind the chat tab.
  const windows = useMemo(() => all.filter((w) => w.role !== "agent"), [all]);
  const docs = useDocs((d) => d.docs[s.id] ?? noDocs);
  const active = useDocs((d) => d.active[s.id]);
  const setActiveIn = useDocs((d) => d.setActive);
  const closeDoc = useDocs((d) => d.close);
  const setActive = (id: string) => setActiveIn(s.id, id);
  const buffers = useBuffers((b) => b.buffers);
  const [visited, setVisited] = useState<string[]>([]);
  const [raw, setRaw] = useState(false);

  // Start on the chat, else whatever tmux has current.
  useEffect(() => {
    if (!env.data) return;
    if (active === chatTab && hasChat) return;
    if (active && (windows.some((w) => w.id === active) || docs.some((d) => d.id === active))) return;
    const pick = hasChat ? chatTab : (windows.find((w) => w.active) ?? windows[0])?.id;
    if (pick) setActiveIn(s.id, pick);
  }, [env.data, windows, docs, active, hasChat, s.id, setActiveIn]);
  useEffect(() => {
    if (!openWindow) return;
    if (openWindow === agentWindow?.id) {
      setActiveIn(s.id, chatTab);
      setRaw(true);
    } else setActiveIn(s.id, openWindow);
  }, [openWindow, agentWindow?.id, s.id, setActiveIn]);
  useEffect(() => {
    const key = active === chatTab && raw ? `${chatTab}:raw` : active;
    if (key && key !== chatTab && !visited.includes(key)) setVisited((v) => [...v, key]);
  }, [active, raw, visited]);

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
      <div
        role="tablist"
        className="flex h-9 shrink-0 items-center gap-0.5 overflow-x-auto border-b border-separator px-2 [scrollbar-width:none]"
      >
        {hasChat && (
          <div
            className={`flex h-7 shrink-0 items-center rounded-ctl ${active === chatTab ? "bg-selected" : "hover:bg-hover"}`}
          >
            <button
              type="button"
              role="tab"
              aria-selected={active === chatTab}
              onClick={() => setActive(chatTab)}
              className={`flex items-center gap-1.5 px-2.5 text-sm ${active === chatTab ? "text-label" : "text-secondary"}`}
            >
              <MessageSquare size={13} />
              Chat
            </button>
          </div>
        )}
        {windows.map((w) => {
          const on = w.id === active;
          const Icon = w.role === "setup" ? Wrench : SquareTerminal;
          return (
            <div
              key={w.id}
              className={`group flex h-7 shrink-0 items-center rounded-ctl ${on ? "bg-selected" : "hover:bg-hover"}`}
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
        <IconButton label="New terminal" className="size-7 shrink-0" onPress={() => void add()}>
          <Plus size={14} />
        </IconButton>
        {docs.length > 0 && <div className="mx-1 h-4 w-px shrink-0 bg-separator" />}
        {docs.map((d) => {
          const on = d.id === active;
          const Icon = d.kind === "diff" ? GitCompareArrows : FileCode;
          const dirty = d.kind === "file" && !!buffers[bufferKey(s.id, d.path)];
          return (
            <div
              key={d.id}
              title={d.path}
              className={`group flex h-7 shrink-0 items-center rounded-ctl ${on ? "bg-selected" : "hover:bg-hover"}`}
            >
              <button
                type="button"
                role="tab"
                aria-selected={on}
                onClick={() => setActive(d.id)}
                className={`flex items-center gap-1.5 pr-1 pl-2.5 text-sm ${on ? "text-label" : "text-secondary"}`}
              >
                <Icon size={13} />
                <span className="max-w-40 truncate">{baseName(d.path)}</span>
                {dirty && <span className="size-1.5 rounded-full bg-label/60" title="Edited" />}
              </button>
              <IconButton
                label={`Close ${baseName(d.path)}`}
                className="mr-0.5 size-5 opacity-0 group-hover:opacity-100"
                onPress={async () => {
                  if (d.kind === "file" && !(await confirmClose(s.id, d.path))) return;
                  closeDoc(s.id, d.id);
                }}
              >
                <X size={11} />
              </IconButton>
            </div>
          );
        })}
        <div className="flex-1" />
        {active === chatTab && agentWindow && (
          <fieldset
            aria-label="Show"
            className="m-0 flex h-6 min-w-0 rounded-ctl border-0 bg-selected p-0.5 text-xs"
          >
            {[
              [false, "Chat"],
              [true, "Terminal"],
            ].map(([on, title]) => (
              <button
                key={String(title)}
                type="button"
                aria-pressed={raw === on}
                onClick={() => setRaw(on as boolean)}
                className={`rounded-[4px] px-2 ${raw === on ? "bg-control text-label shadow-sm" : "text-secondary"}`}
              >
                {title}
              </button>
            ))}
          </fieldset>
        )}
      </div>
      <div className="relative min-h-0 flex-1">
        {hasChat && (
          <div className="absolute inset-0" hidden={active !== chatTab || (raw && !!agentWindow)}>
            <ChatView
              session={s}
              visible={active === chatTab && !raw}
              onTerminal={agentWindow ? () => setRaw(true) : undefined}
            />
          </div>
        )}
        {agentWindow && visited.includes(`${chatTab}:raw`) && (
          <div className="absolute inset-0" hidden={active !== chatTab || !raw}>
            <TerminalView sessionId={s.id} window={agentWindow.id} visible={active === chatTab && raw} />
          </div>
        )}
        {visited
          .filter((id) => windows.some((w) => w.id === id))
          .map((id) => (
            <div key={id} className="absolute inset-0">
              <TerminalView sessionId={s.id} window={id} visible={id === active} />
            </div>
          ))}
        {docs
          .filter((d) => d.id === active)
          .map((d) => (
            <div key={d.id} className="absolute inset-0 bg-surface">
              {d.kind === "diff" ? (
                <DiffView session={s} doc={d} visible />
              ) : (
                <FileEditor session={s} doc={d} visible />
              )}
            </div>
          ))}
      </div>
    </div>
  );
}
