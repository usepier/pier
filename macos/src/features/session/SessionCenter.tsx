import {
  FileCode,
  GitCompareArrows,
  MessagesSquare,
  Plus,
  SquareTerminal,
  TerminalSquare,
  Wrench,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { Button as AriaButton } from "react-aria-components";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { ChatSummary, Session, Window } from "@/api/protocol.gen";
import { registerCommands } from "@/app/commands";
import { agentName, ChatView } from "@/features/chat/ChatView";
import { confirmClose, FileEditor } from "@/features/files/FileEditor";
import { DiffView } from "@/features/review/DiffView";
import { TerminalView } from "@/features/terminal/TerminalView";
import { confirm } from "@/lib/confirm";
import { focusTarget } from "@/lib/focus";
import { readable } from "@/lib/session";
import { seenKey, useSeen } from "@/state/attention";
import { bufferKey, useBuffers } from "@/state/buffers";
import { useChats } from "@/state/chat";
import { chatTab, type Doc, useDocs } from "@/state/docs";
import { useNewChats, useNewChatsOf } from "@/state/newChats";
import { useChatDefaults } from "@/state/settings";
import { attempt } from "@/state/toasts";
import { IconButton } from "@/ui/Button";
import { Menu } from "@/ui/Menu";
import { Spinner } from "@/ui/Spinner";
import { Tip } from "@/ui/Tooltip";

const shells = new Set(["bash", "zsh", "sh", "fish", "-bash", "-zsh"]);

function label(w: Window): string {
  if (w.role === "agent") return "Chats";
  if (w.role === "setup") return w.name === "setup-failed" ? "Setup failed" : "Setup";
  if (w.role === "home" && shells.has(w.command)) return "Shell";
  return w.name || w.command || w.id;
}

const noDocs: Doc[] = [];
const isChatTab = (t?: string) => !!t?.startsWith("chat:");

function baseName(p: string) {
  return p.slice(p.lastIndexOf("/") + 1);
}

/** A chat's mark on its tab: working, waiting on you, unread. */
function ChatMark({ c, unread }: { c?: ChatSummary; unread: boolean }) {
  if (!c) return <span className="size-1.5 rounded-full bg-quaternary" />;
  if (c.state === "working") return <Spinner size={10} />;
  if (c.state === "waiting")
    return <span className="size-1.5 rounded-full bg-warn" title="Waiting for you" />;
  if (c.state === "error") return <span className="size-1.5 rounded-full bg-bad" title="Failing" />;
  if (c.state === "terminal") return <TerminalSquare size={11} className="text-tertiary" />;
  if (unread) return <span className="size-1.5 rounded-full bg-accent" title="Finished — not seen yet" />;
  return null;
}

function Tab({
  id,
  on,
  onSelect,
  icon,
  title,
  children,
  onClose,
  closeLabel,
  hint,
}: {
  /** What setActive takes to show it. */
  id: string;
  on: boolean;
  onSelect: () => void;
  icon?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  onClose?: () => void;
  closeLabel?: string;
  hint?: string;
}) {
  return (
    <div
      title={hint}
      data-tab-id={id}
      className={`group flex h-[30px] max-w-[220px] min-w-[120px] shrink items-center rounded-[7px] transition-colors ${on ? "bg-selected" : "hover:bg-hover"}`}
    >
      <button
        type="button"
        role="tab"
        aria-selected={on}
        tabIndex={on ? 0 : -1}
        aria-keyshortcuts={onClose ? "Delete" : undefined}
        onClick={onSelect}
        onKeyDown={(e) => {
          if (onClose && (e.key === "Delete" || e.key === "Backspace")) {
            e.preventDefault();
            onClose();
          }
        }}
        className={`flex h-full min-w-0 flex-1 items-center gap-1.5 pl-2.5 text-sm ${on ? "text-label" : "text-secondary group-hover:text-label"} ${onClose ? "pr-1" : "pr-2.5"}`}
      >
        <span className="flex shrink-0 items-center">{icon}</span>
        <span className="min-w-0 truncate">{title}</span>
        {children}
      </button>
      {onClose && (
        // The pointer's way to close; the keyboard's is Delete on the tab
        // (or ⌘W), so the tab list holds only tabs.
        <button
          type="button"
          aria-hidden
          tabIndex={-1}
          title={closeLabel ?? "Close"}
          onClick={onClose}
          className={`mr-1 inline-flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-ctl text-tertiary transition-colors hover:bg-hover hover:text-label ${on ? "opacity-60" : "opacity-0"} group-hover:opacity-100`}
        >
          <X size={11} />
        </button>
      )}
    </div>
  );
}

/**
 * The session's tabs: its chats first, then its terminals (tmux windows),
 * then the diffs and files opened from the inspector. + opens a chat with
 * either agent, or a terminal.
 */
export function SessionCenter({ session: s, openWindow }: { session: Session; openWindow?: string }) {
  const live = readable(s);
  const env = useWatch("env", { id: s.id }, { enabled: live });
  const docs = useDocs((d) => d.docs[s.id] ?? noDocs);
  const active = useDocs((d) => d.active[s.id]);
  // Tabs act at once: one opening shows (and is shown) before the session
  // has made it, one closing goes before the session has; the session's
  // next look (every few seconds) then agrees.
  const [opening, setOpening] = useState<{ id: string; title: string; agent?: string }[]>([]);
  const [made, setMade] = useState<Window[]>([]);
  const [closing, setClosing] = useState<Set<string>>(() => new Set());
  // The session's own shell (main's first window) is a tab only when
  // something runs in it or it was opened: terminals are the ones asked for.
  const windows = useMemo(() => {
    const listed = env.data?.windows ?? [];
    const ids = new Set(listed.map((w) => w.id));
    return [...listed, ...made.filter((w) => !ids.has(w.id))]
      .filter((w) => !closing.has(w.id))
      .filter((w) => w.role !== "home" || (w.command && !shells.has(w.command)) || w.id === active)
      .sort((a, b) => a.index - b.index);
  }, [env.data, made, closing, active]);
  // Once the session's look has caught up, what was waiting on it goes.
  useEffect(() => {
    const listed = new Set((env.data?.windows ?? []).map((w) => w.id));
    setMade((m) => (m.some((w) => listed.has(w.id)) ? m.filter((w) => !listed.has(w.id)) : m));
    setClosing((c) => {
      const gone = [...c].filter((id) => id.startsWith("@") && !listed.has(id));
      if (!gone.length) return c;
      const next = new Set(c);
      for (const id of gone) next.delete(id);
      return next;
    });
  }, [env.data]);
  const list = useChats(s.id);
  const allChats = list.chats.length ? list.chats : (s.chats ?? []);
  const chats = useMemo(() => allChats.filter((c) => !closing.has(chatTab(c.id))), [allChats, closing]);
  useEffect(() => {
    const ids = new Set(allChats.map((c) => chatTab(c.id)));
    setClosing((c) => {
      const gone = [...c].filter((id) => isChatTab(id) && !ids.has(id));
      if (!gone.length) return c;
      const next = new Set(c);
      for (const id of gone) next.delete(id);
      return next;
    });
  }, [allChats]);
  const drafts = useNewChatsOf(s.id);
  const openDraft = useNewChats((n) => n.open);
  const dropDraft = useNewChats((n) => n.drop);
  const defaults = useChatDefaults();
  const seen = useSeen((x) => x.seen);
  const setActiveIn = useDocs((d) => d.setActive);
  const closeDoc = useDocs((d) => d.close);
  const setActive = (id: string) => setActiveIn(s.id, id);
  const buffers = useBuffers((b) => b.buffers);
  const [visited, setVisited] = useState<string[]>([]);
  const strip = useRef<HTMLDivElement>(null);

  // A draft whose chat started is the chat now.
  useEffect(() => {
    for (const d of drafts) if (chats.some((c) => c.id === d.id)) dropDraft(s.id, d.id);
  }, [drafts, chats, s.id, dropDraft]);
  const openDrafts = drafts.filter((d) => !chats.some((c) => c.id === d.id));

  const chatIds = useMemo(
    () => [...chats.map((c) => c.id), ...openDrafts.map((d) => d.id)],
    [chats, openDrafts],
  );
  const order = useMemo(
    () => [
      ...chatIds.map(chatTab),
      ...windows.map((w) => w.id),
      ...opening.map((o) => o.id),
      ...docs.map((d) => d.id),
    ],
    [chatIds, windows, opening, docs],
  );

  // Start on a chat (one waiting on you first), else whatever tmux has current.
  useEffect(() => {
    if (active && order.includes(active)) return;
    if (active?.startsWith("@") && live && !env.data) return; // its window is still loading
    const waiting = chats.find((c) => c.state === "waiting");
    const pick = waiting
      ? chatTab(waiting.id)
      : chatIds[0]
        ? chatTab(chatIds[0])
        : (windows.find((w) => w.active) ?? windows[0])?.id;
    if (pick) setActiveIn(s.id, pick);
  }, [order, active, chats, chatIds, windows, live, env.data, s.id, setActiveIn]);
  useEffect(() => {
    if (openWindow) setActiveIn(s.id, openWindow);
  }, [openWindow, s.id, setActiveIn]);
  useEffect(() => {
    if (active && !isChatTab(active) && !visited.includes(active)) setVisited((v) => [...v, active]);
  }, [active, visited]);

  const newChat = (agent?: string) => {
    const d = defaults(agent);
    const id = crypto.randomUUID().replace(/-/g, "").slice(0, 16);
    openDraft(s.id, { id, ...d });
    setActive(chatTab(id));
    focusTarget("composer", { next: true });
  };
  // A terminal tab shows the moment it's asked for, opening; the session's
  // window takes its place once made — shown, unless another tab was picked
  // meanwhile. If it can't be made, the tab goes and the one before is back.
  const openTab = async (title: string, make: () => Promise<Window | undefined>, agent?: string) => {
    const temp = `opening:${crypto.randomUUID()}`;
    const before = active;
    setOpening((o) => [...o, { id: temp, title, agent }]);
    setActive(temp);
    const w = await make();
    setOpening((o) => o.filter((x) => x.id !== temp));
    const still = useDocs.getState().active[s.id] === temp;
    if (!w) {
      if (still && before) setActive(before);
      return;
    }
    setMade((m) => [...m.filter((x) => x.id !== w.id), w]);
    if (still) setActive(w.id);
  };
  const newTerminal = () =>
    openTab("Terminal", () => attempt(() => rpc.call("tmux.newWindow", { sessionId: s.id })));
  const chatsInTerminal = () =>
    openTab("Chats", async () => {
      const r = await attempt(() => rpc.call("chats.window", { id: s.id }));
      return r && { id: r.windowId, index: 1e6, name: "chats", command: "", active: false, role: "agent" };
    });
  const takeover = (chatId: string) => {
    const c = chats.find((x) => x.id === chatId);
    const name = c ? agentName(c.agent) : "Agent";
    return openTab(
      name,
      async () => {
        const r = await attempt(() => rpc.call("chat.takeover", { sessionId: s.id, chatId }));
        return (
          r && { id: r.windowId, index: 1e6, name: name.toLowerCase(), command: "", active: false, role: "" }
        );
      },
      c?.agent,
    );
  };

  // A tab closing goes at once — its neighbour shown if it was — and comes
  // back, with why, if the session can't close it.
  const closeNow = async (id: string, close: () => Promise<unknown>) => {
    if (active === id) {
      const at = order.indexOf(id);
      const next = order[at + 1] ?? order[at - 1];
      if (next) setActive(next);
    }
    setClosing((c) => new Set(c).add(id));
    if ((await attempt(close)) === undefined) {
      setClosing((c) => {
        const next = new Set(c);
        next.delete(id);
        return next;
      });
    }
  };

  const activeTabButton = () =>
    active
      ? strip.current?.querySelector<HTMLButtonElement>(`[data-tab-id="${CSS.escape(active)}"] [role="tab"]`)
      : undefined;

  // ⌘⇧] and ⌘⇧[ step through the tabs; ⌘L goes to the chat's message box;
  // ⌘T opens a chat.
  useEffect(() => {
    const step = (by: number) => {
      const at = order.indexOf(active ?? "");
      const next = order[(at + by + order.length) % order.length];
      if (next) setActiveIn(s.id, next);
    };
    return registerCommands([
      {
        id: "view.nextTab",
        title: "Next Tab",
        shortcut: "CmdOrCtrl+Shift+]",
        menu: "view",
        run: () => step(1),
      },
      {
        id: "view.prevTab",
        title: "Previous Tab",
        shortcut: "CmdOrCtrl+Shift+[",
        menu: "view",
        run: () => step(-1),
      },
      {
        id: "session.newChat",
        title: "New Chat",
        shortcut: "CmdOrCtrl+T",
        menu: "session",
        enabled: () => s.hasKey,
        run: () => newChat(),
      },
      {
        id: "session.message",
        title: "Message the Agent",
        shortcut: "CmdOrCtrl+L",
        menu: "session",
        enabled: () => chatIds.length > 0,
        run: () => {
          if (!isChatTab(active) && chatIds[0]) setActiveIn(s.id, chatTab(chatIds[0]));
          focusTarget("composer");
        },
      },
      {
        id: "view.closeTab",
        title: "Close Tab",
        shortcut: "CmdOrCtrl+W",
        menu: "view",
        // The tab closes as Delete on it would (and asks what it would).
        enabled: () => !!activeTabButton()?.getAttribute("aria-keyshortcuts"),
        run: () => {
          activeTabButton()?.dispatchEvent(new KeyboardEvent("keydown", { key: "Delete", bubbles: true }));
        },
      },
    ]);
  });

  const closeChat = async (c: ChatSummary) => {
    const ok = await confirm(
      `Close “${c.title}”?`,
      c.state === "working" || c.state === "waiting"
        ? `${agentName(c.agent)} is mid-turn; closing stops it. The conversation is kept in the session.`
        : "The conversation is kept in the session; the chat leaves this list.",
      { ok: "Close", destructive: c.state === "working" || c.state === "waiting" },
    );
    if (!ok) return;
    await closeNow(chatTab(c.id), () => rpc.call("chat.close", { sessionId: s.id, chatId: c.id }));
  };
  const closeWindow = async (w: Window) => {
    const busy = w.command && !shells.has(w.command);
    if (
      busy &&
      !(await confirm(`Close ${label(w)}?`, `${w.command} is still running in it; closing ends it.`, {
        ok: "Close",
        destructive: true,
      }))
    )
      return;
    setVisited((v) => v.filter((x) => x !== w.id));
    await closeNow(w.id, () => rpc.call("tmux.killWindow", { sessionId: s.id, window: w.id }));
  };

  const activeChat = isChatTab(active) ? active!.slice(5) : undefined;
  // The active tab stays in view however many there are.
  useEffect(() => {
    if (!active) return;
    strip.current
      ?.querySelector(`[data-tab-id="${CSS.escape(active)}"]`)
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [active]);
  return (
    <div className="flex h-full flex-col bg-surface">
      <div className="flex h-11 shrink-0 items-center border-b border-separator">
        <div
          ref={strip}
          role="tablist"
          aria-label="Chats, terminals and files"
          onWheel={(e) => {
            // A mouse wheel scrolls the tabs sideways.
            if (Math.abs(e.deltaY) > Math.abs(e.deltaX)) e.currentTarget.scrollLeft += e.deltaY;
          }}
          onKeyDown={(e) => {
            // Arrows move along the tabs (and show the one reached).
            const tabs = [...e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
            const at = tabs.indexOf(document.activeElement as HTMLButtonElement);
            if (at < 0) return;
            const to =
              e.key === "ArrowRight"
                ? (at + 1) % tabs.length
                : e.key === "ArrowLeft"
                  ? (at - 1 + tabs.length) % tabs.length
                  : e.key === "Home"
                    ? 0
                    : e.key === "End"
                      ? tabs.length - 1
                      : -1;
            if (to < 0) return;
            e.preventDefault();
            tabs[to]!.focus();
            tabs[to]!.click();
          }}
          className="flex h-full min-w-0 flex-1 items-center gap-1 overflow-x-auto px-2.5 [scrollbar-width:none]"
        >
          {chats.map((c) => (
            <Tab
              key={c.id}
              id={chatTab(c.id)}
              on={active === chatTab(c.id)}
              onSelect={() => setActive(chatTab(c.id))}
              icon={<AgentGlyph agent={c.agent} />}
              title={c.title || "New chat"}
              hint={`${agentName(c.agent)} · ${c.title}`}
              onClose={() => void closeChat(c)}
              closeLabel={`Close ${c.title}`}
            >
              <ChatMark
                c={c}
                unread={c.state === "ready" && !!c.last && c.since > (seen[seenKey(s.id, c.id)] ?? "")}
              />
            </Tab>
          ))}
          {openDrafts.map((d) => (
            <Tab
              key={d.id}
              id={chatTab(d.id)}
              on={active === chatTab(d.id)}
              onSelect={() => setActive(chatTab(d.id))}
              icon={<AgentGlyph agent={d.agent} />}
              title="New chat"
              onClose={() => dropDraft(s.id, d.id)}
              closeLabel="Discard the new chat"
            />
          ))}
          {(chats.length > 0 || openDrafts.length > 0) && windows.length > 0 && (
            <div aria-hidden className="mx-1 h-4 w-px shrink-0 bg-separator" />
          )}
          {windows.map((w) => {
            const Icon = w.role === "setup" ? Wrench : w.role === "agent" ? MessagesSquare : SquareTerminal;
            return (
              <Tab
                key={w.id}
                id={w.id}
                on={w.id === active}
                onSelect={() => setActive(w.id)}
                icon={<Icon size={13} className={w.name === "setup-failed" ? "text-bad" : ""} />}
                title={label(w)}
                onClose={w.role === "setup" ? undefined : () => void closeWindow(w)}
                closeLabel={`Close ${label(w)}`}
              />
            );
          })}
          {opening.map((o) => (
            <Tab
              key={o.id}
              id={o.id}
              on={o.id === active}
              onSelect={() => setActive(o.id)}
              icon={o.agent ? <AgentGlyph agent={o.agent} /> : <SquareTerminal size={13} />}
              title={o.title}
            >
              <Spinner size={10} />
            </Tab>
          ))}
          {docs.length > 0 && <div aria-hidden className="mx-1 h-4 w-px shrink-0 bg-separator" />}
          {docs.map((d) => {
            const Icon = d.kind === "diff" ? GitCompareArrows : FileCode;
            const dirty = d.kind === "file" && !!buffers[bufferKey(s.id, d.path)];
            return (
              <Tab
                key={d.id}
                id={d.id}
                on={d.id === active}
                onSelect={() => setActive(d.id)}
                icon={<Icon size={13} />}
                title={baseName(d.path)}
                hint={d.path}
                onClose={async () => {
                  if (d.kind === "file" && !(await confirmClose(s.id, d.path))) return;
                  closeDoc(s.id, d.id);
                }}
                closeLabel={`Close ${baseName(d.path)}`}
              >
                {dirty && <span className="size-1.5 rounded-full bg-label/60" title="Edited" />}
              </Tab>
            );
          })}
        </div>
        <div className="flex shrink-0 items-center gap-1 pr-2 pl-1">
          <Menu
            label="Open"
            placement="bottom start"
            trigger={
              <AriaButton
                aria-label="New chat or terminal"
                isDisabled={!s.hasKey}
                className="inline-flex size-[30px] shrink-0 cursor-pointer items-center justify-center rounded-[7px] text-tertiary outline-none transition-colors hover:bg-hover hover:text-label disabled:cursor-default disabled:opacity-35"
              >
                <Plus size={15} />
              </AriaButton>
            }
            items={[
              {
                id: "claude",
                label: "New Claude chat",
                icon: <AgentGlyph agent="claude" />,
                onAction: () => newChat("claude"),
              },
              {
                id: "codex",
                label: "New Codex chat",
                icon: <AgentGlyph agent="codex" />,
                onAction: () => newChat("codex"),
              },
              "separator",
              {
                id: "term",
                label: "New terminal",
                icon: <SquareTerminal size={13} />,
                disabled: !live,
                onAction: () => void newTerminal(),
              },
              {
                id: "chats-term",
                label: "Chats in a terminal",
                icon: <MessagesSquare size={13} />,
                disabled: !live,
                onAction: () => void chatsInTerminal(),
              },
            ]}
          />
          {activeChat && live && chats.some((c) => c.id === activeChat) && (
            <Tip content="Continue this chat in a terminal (the agent's own interface)">
              <IconButton label="Continue in a terminal" onPress={() => void takeover(activeChat)}>
                <TerminalSquare size={15} />
              </IconButton>
            </Tip>
          )}
        </div>
      </div>
      <div className="relative min-h-0 flex-1">
        {chatIds.map((id) => (
          <div key={id} className="absolute inset-0" hidden={active !== chatTab(id)}>
            {active === chatTab(id) && (
              <ChatView
                session={s}
                chatId={id}
                draft={openDrafts.find((d) => d.id === id)}
                visible
                onTakeover={() => void takeover(id)}
              />
            )}
          </div>
        ))}
        {chatIds.length === 0 && !windows.length && !opening.length && !docs.length && (
          <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
            <MessagesSquare size={28} strokeWidth={1.5} className="text-quaternary" />
            <div className="text-base text-secondary">No chats in this session yet.</div>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => newChat("claude")}
                className="rounded-ctl border border-strong px-3 py-1.5 text-sm transition-colors hover:bg-hover"
              >
                Chat with Claude
              </button>
              <button
                type="button"
                onClick={() => newChat("codex")}
                className="rounded-ctl border border-strong px-3 py-1.5 text-sm transition-colors hover:bg-hover"
              >
                Chat with Codex
              </button>
            </div>
          </div>
        )}
        {opening
          .filter((o) => o.id === active)
          .map((o) => (
            <div
              key={o.id}
              className="absolute inset-0 flex items-center justify-center gap-2 text-sm text-secondary"
            >
              <Spinner /> Opening {o.agent ? `${o.title} in a terminal` : "a terminal"}…
            </div>
          ))}
        {live &&
          visited
            .filter((id) => id.startsWith("@"))
            .map((id) => (
              <div key={id} className="absolute inset-0" hidden={id !== active}>
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

/** A small mark for an agent. */
export function AgentGlyph({ agent }: { agent: string }) {
  return (
    <span
      aria-hidden="true"
      className={`inline-flex size-[15px] shrink-0 items-center justify-center rounded-[4px] text-[9px] font-bold ${agent === "codex" ? "bg-label text-surface" : "bg-[#d97757] text-white"}`}
    >
      {agent === "codex" ? "X" : "C"}
    </span>
  );
}
