// How a transcript reads: an agent's run of tool calls between two
// messages folds into one "steps" block once it is done, so a long turn
// reads as what was said and what was done.
import type { ChatItem, ToolCall } from "@/api/protocol.gen";

export type Block = { kind: "item"; item: ChatItem } | { kind: "steps"; id: string; items: ChatItem[] };

/** Runs shorter than this stay as they are. */
const minRun = 3;

function stepLike(i: ChatItem): boolean {
  if (i.kind === "thinking") return true;
  // The task list, questions and plans stay in view: they are the
  // conversation, not its steps.
  return i.kind === "tool" && !["todo", "ask", "plan"].includes(i.tool?.view ?? "");
}

/** Blocks of a transcript; expand keeps every step in view (no folding). */
export function groupItems(items: ChatItem[], expand = false): Block[] {
  if (expand) return items.map((item) => ({ kind: "item", item }));
  const out: Block[] = [];
  let run: ChatItem[] = [];
  const flush = (last: boolean) => {
    // The run still being worked on stays open; finished ones fold.
    const busy = run.some((i) => i.tool?.status === "running");
    if (run.length >= minRun && !(last && busy)) {
      out.push({ kind: "steps", id: run[0]!.id, items: run });
    } else {
      for (const item of run) out.push({ kind: "item", item });
    }
    run = [];
  };
  for (const item of items) {
    if (stepLike(item)) {
      run.push(item);
      continue;
    }
    if (run.length) flush(false);
    out.push({ kind: "item", item });
  }
  if (run.length) flush(true);
  return out;
}

const verbs: Record<string, [string, string]> = {
  bash: ["ran", "command"],
  edit: ["edited", "file"],
  write: ["created", "file"],
  read: ["read", "file"],
  search: ["ran", "search"],
  web: ["looked up", "page"],
  task: ["delegated", "task"],
};

/** "ran 3 commands, edited 2 files" */
export function summarize(items: ChatItem[]): string {
  const counts = new Map<string, number>();
  let other = 0;
  for (const i of items) {
    const v = i.tool?.view;
    if (!v) continue;
    if (v in verbs) counts.set(v, (counts.get(v) ?? 0) + 1);
    else other++;
  }
  const parts = [...counts].map(([v, n]) => {
    const [verb, noun] = verbs[v]!;
    return `${verb} ${n} ${n === 1 ? noun : `${noun}s`}`;
  });
  if (other) parts.push(`used ${other} ${other === 1 ? "tool" : "tools"}`);
  const s = parts.join(", ") || "thought it over";
  return s[0]!.toUpperCase() + s.slice(1);
}

/** Whether any step failed, for the folded block's mark. */
export function anyFailed(items: ChatItem[]): boolean {
  return items.some((i) => (i.tool as ToolCall | undefined)?.status === "error");
}
