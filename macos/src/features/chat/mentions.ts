// @-mentions in the composer: which word is being typed, which files match
// it, and the text with one picked.

/** The @-query at the caret, if the caret ends one. */
export function mentionAt(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret);
  const m = before.match(/(^|\s)@([^\s@]*)$/);
  if (!m) return null;
  return { start: caret - m[2]!.length - 1, query: m[2]! };
}

/** Puts a picked path in place of the @-query; returns the text and the caret after it. */
export function insertMention(text: string, at: { start: number; query: string }, path: string) {
  const end = at.start + 1 + at.query.length;
  const ins = `@${path} `;
  return { text: text.slice(0, at.start) + ins + text.slice(end), caret: at.start + ins.length };
}

/** Scores a path against a query: higher is better, -1 for no match. */
export function score(path: string, query: string): number {
  if (!query) return 1;
  const p = path.toLowerCase();
  const q = query.toLowerCase();
  const base = p.slice(p.lastIndexOf("/") + 1);
  if (base.startsWith(q)) return 1000 - path.length;
  const i = p.indexOf(q);
  if (i >= 0) return 500 - i - path.length / 10;
  // Every character in order (fuzzy), favouring tight matches.
  let at = -1;
  let gaps = 0;
  for (const ch of q) {
    const n = p.indexOf(ch, at + 1);
    if (n < 0) return -1;
    if (at >= 0) gaps += n - at - 1;
    at = n;
  }
  return 100 - gaps - path.length / 10;
}

export function rank(paths: string[], query: string, limit = 8): string[] {
  const scored: [number, string][] = [];
  for (const p of paths) {
    const s = score(p, query);
    if (s >= 0) scored.push([s, p]);
  }
  scored.sort((a, b) => b[0] - a[0] || a[1].localeCompare(b[1]));
  return scored.slice(0, limit).map(([, p]) => p);
}
