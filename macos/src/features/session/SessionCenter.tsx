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
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { Button as AriaButton } from "react-aria-components";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { ChatSummary, Session, Window } from "@/api/protocol.gen";
import { registerCommands } from "@/app/commands";
import { agentName, ChatView, running } from "@/features/chat/ChatView";
import { confirmClose, FileEditor } from "@/features/files/FileEditor";
import { DiffView } from "@/features/review/DiffView";
import { TerminalView } from "@/features/terminal/TerminalView";
import { confirm } from "@/lib/confirm";
import { seenKey, useSeen } from "@/state/attention";
import { bufferKey, useBuffers } from "@/state/buffers";
import { useChats } from "@/state/chat";
import { type Doc, useDocs } from "@/state/docs";
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
  return w.name || w.command || w.id;
}

const noDocs: Doc[] = [];
const chatTab = (id: string) => `chat:${id}`;
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
  on,
  onSelect,
  icon,
  title,
  children,
  onClose,
  closeLabel,
  hint,
}: {
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
      className={`group flex h-[30px] shrink-0 items-center rounded-[7px] transition-colors ${on ? "bg-selected" : "hover:bg-hover"}`}
    >
      <button
        type="button"
        role="tab"
        aria-selected={on}
        onClick={onSelect}
        className={`flex h-full items-center gap-1.5 pl-2.5 text-sm ${on ? "text-label" : "text-secondary group-hover:text-label"} ${onClose ? "pr-1" : "pr-2.5"}`}
      >
        {icon}
        <span className="max-w-44 truncate">{title}</span>
        {children}
      </button>
      {onClose && (
        <IconButton
          label={closeLabel ?? "Close"}
          size="sm"
          className={`mr-1 size-5 ${on ? "opacity-60" : "opacity-0"} group-hover:opacity-100`}
          onPress={onClose}
        >
          <X size={11} />
        </IconButton>
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
  const live = running(s) && s.hasKey;
  const env = useWatch("env", { id: s.id }, { enabled: live });
  const windows = useMemo(() => [...(env.data?.windows ?? [])].sort((a, b) => a.index - b.index), [env.data]);
  const list = useChats(s.id);
  const chats = list.chats.length ? list.chats : (s.chats ?? []);
  const drafts = useNewChatsOf(s.id);
  const openDraft = useNewChats((n) => n.open);
  const dropDraft = useNewChats((n) => n.drop);
  const defaults = useChatDefaults();
  const seen = useSeen((x) => x.seen);
  const docs = useDocs((d) => d.docs[s.id] ?? noDocs);
  const active = useDocs((d) => d.active[s.id]);
  const setActiveIn = useDocs((d) => d.setActive);
  const closeDoc = useDocs((d) => d.close);
  const setActive = (id: string) => setActiveIn(s.id, id);
  const buffers = useBuffers((b) => b.buffers);
  const [visited, setVisited] = useState<string[]>([]);

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
    () => [...chatIds.map(chatTab), ...windows.map((w) => w.id), ...docs.map((d) => d.id)],
    [chatIds, windows, docs],
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
    requestAnimationFrame(() =>
      document.querySelector<HTMLTextAreaElement>("textarea[aria-label='Message']")?.focus(),
    );
  };
  const newTerminal = async () => {
    const w = await attempt(() => rpc.call("tmux.newWindow", { sessionId: s.id }));
    if (w) setActive(w.id);
  };
  const chatsInTerminal = async () => {
    const r = await attempt(() => rpc.call("chats.window", { id: s.id }));
    if (r) setActive(r.windowId);
  };
  const takeover = async (chatId: string) => {
    const r = await attempt(() => rpc.call("chat.takeover", { sessionId: s.id, chatId }));
    if (r) setActive(r.windowId);
  };

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
          requestAnimationFrame(() =>
            document.querySelector<HTMLTextAreaElement>("textarea[aria-label='Message']")?.focus(),
          );
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
    await attempt(() => rpc.call("chat.close", { sessionId: s.id, chatId: c.id }));
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
    await attempt(() => rpc.call("tmux.killWindow", { sessionId: s.id, window: w.id }));
    setVisited((v) => v.filter((x) => x !== w.id));
  };

  const activeChat = isChatTab(active) ? active!.slice(5) : undefined;
  return (
    <div className="flex h-full flex-col bg-surface">
      <div
        role="tablist"
        className="flex h-11 shrink-0 items-center gap-1 overflow-x-auto border-b border-separator px-2.5 [scrollbar-width:none]"
      >
        {chats.map((c) => (
          <Tab
            key={c.id}
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
            on={active === chatTab(d.id)}
            onSelect={() => setActive(chatTab(d.id))}
            icon={<AgentGlyph agent={d.agent} />}
            title="New chat"
            onClose={() => dropDraft(s.id, d.id)}
            closeLabel="Discard the new chat"
          />
        ))}
        {(chats.length > 0 || openDrafts.length > 0) && windows.length > 0 && (
          <div className="mx-1 h-4 w-px shrink-0 bg-separator" />
        )}
        {windows.map((w) => {
          const Icon = w.role === "setup" ? Wrench : w.role === "agent" ? MessagesSquare : SquareTerminal;
          return (
            <Tab
              key={w.id}
              on={w.id === active}
              onSelect={() => setActive(w.id)}
              icon={<Icon size={13} className={w.name === "setup-failed" ? "text-bad" : ""} />}
              title={label(w)}
              onClose={w.role === "setup" ? undefined : () => void closeWindow(w)}
              closeLabel={`Close ${label(w)}`}
            />
          );
        })}
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
        {docs.length > 0 && <div className="mx-1 h-4 w-px shrink-0 bg-separator" />}
        {docs.map((d) => {
          const Icon = d.kind === "diff" ? GitCompareArrows : FileCode;
          const dirty = d.kind === "file" && !!buffers[bufferKey(s.id, d.path)];
          return (
            <Tab
              key={d.id}
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
        <div className="flex-1" />
        {activeChat && live && chats.some((c) => c.id === activeChat) && (
          <Tip content="Continue this chat in a terminal (the agent's own interface)">
            <IconButton label="Continue in a terminal" onPress={() => void takeover(activeChat)}>
              <TerminalSquare size={15} />
            </IconButton>
          </Tip>
        )}
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
        {chatIds.length === 0 && !windows.length && !docs.length && (
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
