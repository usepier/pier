// The chat a session's "send to the agent" goes to: the chat tab open in
// it, else its most recently active chat, else a new one.
import type { Session } from "@/api/protocol.gen";
import { useDocs } from "./docs";
import { type NewChat, useNewChats } from "./newChats";
import { sendPrompt } from "./outbox";

export interface Target {
  chatId: string;
  /** Set when the chat is new: the prompt starts it. */
  draft?: NewChat;
}

export function currentChat(s: Session): Target {
  const active = useDocs.getState().active[s.id];
  const drafts = useNewChats.getState().chats[s.id] ?? [];
  if (active?.startsWith("chat:")) {
    const id = active.slice(5);
    return { chatId: id, draft: drafts.find((d) => d.id === id) };
  }
  const recent = [...(s.chats ?? [])].sort((a, b) => b.since.localeCompare(a.since))[0];
  if (recent) return { chatId: recent.id };
  if (drafts[0]) return { chatId: drafts[0].id, draft: drafts[0] };
  // pier fills the model, effort and mode from the agent settings.
  const draft: NewChat = {
    id: crypto.randomUUID().replace(/-/g, "").slice(0, 16),
    agent: "",
    model: "",
    effort: "",
    mode: "",
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
