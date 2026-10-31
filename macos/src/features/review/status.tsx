import type { ChangedFile } from "@/api/protocol.gen";

const marks: Record<string, [string, string]> = {
  added: ["A", "text-ok"],
  modified: ["M", "text-warn"],
  deleted: ["D", "text-bad"],
  renamed: ["R", "text-info"],
  untracked: ["U", "text-ok"],
  conflicted: ["!", "text-bad"],
};

/** The one-letter mark of a changed file's status. */
export function StatusMark({ status }: { status: string }) {
  const [letter, tone] = marks[status] ?? ["?", "text-tertiary"];
  return (
    <span title={status} className={`w-3 shrink-0 text-center font-mono text-[11px] font-semibold ${tone}`}>
      {letter}
    </span>
  );
}

/** "src/app/" and "main.go", for a dimmed folder and a plain name. */
export function splitPath(p: string): [string, string] {
  const i = p.lastIndexOf("/");
  return i < 0 ? ["", p] : [p.slice(0, i + 1), p.slice(i + 1)];
}

export function totals(files: ChangedFile[]) {
  let added = 0;
  let removed = 0;
  for (const f of files) {
    added += f.added;
    removed += f.removed;
  }
  return { added, removed };
}
