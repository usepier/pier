// The chat a session's "send to the agent" goes to: the chat tab open in
// it, else its most recently active chat, else a new one — never one that
// was closed, though the tab or the session's last look may still name it.
import type { Session, Settings } from "@/api/protocol.gen";
import { queryClient } from "@/api/queryClient";
import { liveChats } from "./chat";
import { useDocs } from "./docs";
import { type NewChat, useNewChats } from "./newChats";
import { sendPrompt } from "./outbox";
import { chatDefaults } from "./settings";

export interface Target {
  chatId: string;
  /** Set when the chat is new: the prompt starts it. */
  draft?: NewChat;
}

export function currentChat(s: Session): Target {
  const active = useDocs.getState().active[s.id];
  const drafts = useNewChats.getState().chats[s.id] ?? [];
  const chats = liveChats(s.id) ?? s.chats ?? [];
  const id = active?.startsWith("chat:") ? active.slice(5) : undefined;
  if (id) {
    const draft = drafts.find((d) => d.id === id);
    if (draft || chats.some((c) => c.id === id)) return { chatId: id, draft };
  }
  const recent = [...chats].sort((a, b) => b.since.localeCompare(a.since))[0];
  if (recent) return { chatId: recent.id };
  if (drafts[0]) return { chatId: drafts[0].id, draft: drafts[0] };
  const draft: NewChat = {
    id: crypto.randomUUID().replace(/-/g, "").slice(0, 16),
    ...chatDefaults(queryClient.getQueryData<Settings>(["settings"])),
  };
  useNewChats.getState().open(s.id, draft);
  return { chatId: draft.id, draft };
}

/** Shows a chat in its session's center. */
export function showChat(sessionId: string, chatId: string) {
  useDocs.getState().setActive(sessionId, `chat:${chatId}`);
}

/** Sends text to the session's current chat and shows it. */
export function sendToAgent(s: Session, text: string) {
  const t = currentChat(s);
  const create = t.draft
    ? { agent: t.draft.agent, model: t.draft.model, effort: t.draft.effort, mode: t.draft.mode }
    : undefined;
  sendPrompt({ sessionId: s.id, chatId: t.chatId, text, mode: "queue", create });
  showChat(s.id, t.chatId);
}
