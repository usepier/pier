// What the palette lists for a query: sessions, repositories and actions,
// best match first. ">" asks for actions only.
import { score } from "@/features/chat/mentions";

export interface Entry {
  id: string;
  kind: "session" | "repo" | "command";
  title: string;
  detail?: string;
  keywords?: string;
  shortcut?: string;
  run: () => void;
}

export function rankEntries(entries: Entry[], raw: string, limit = 40): Entry[] {
  const commandsOnly = raw.startsWith(">");
  const q = (commandsOnly ? raw.slice(1) : raw).trim();
  const pool = commandsOnly ? entries.filter((e) => e.kind === "command") : entries;
  if (!q) return pool.slice(0, limit);
  const scored: [number, Entry][] = [];
  for (const e of pool) {
    const best = Math.max(
      score(e.title, q),
      e.keywords ? score(e.keywords, q) - 50 : -1,
      e.detail ? score(e.detail, q) - 80 : -1,
    );
    if (best >= 0) scored.push([best, e]);
  }
  scored.sort((a, b) => b[0] - a[0] || a[1].title.localeCompare(b[1].title));
  return scored.slice(0, limit).map(([, e]) => e);
}
