// Numbers the way the app shows them: honest, short, never more precise
// than what pier knows.

export function hourly(usd: number): string {
  if (usd <= 0) return "$0/h";
  if (usd < 0.01) return "<$0.01/h";
  return `$${usd < 1 ? usd.toFixed(2) : usd.toFixed(2)}/h`;
}

export function monthly(usd: number): string {
  if (usd <= 0) return "$0/mo";
  if (usd < 10) return `$${usd.toFixed(2)}/mo`;
  return `$${Math.round(usd)}/mo`;
}

/** "3m", "2h", "5d" since a time; "" when unknown. */
export function age(iso?: string | null, now = Date.now()): string {
  if (!iso) return "";
  const s = Math.max(0, (now - Date.parse(iso)) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

/** "3 minutes ago"-style, for "as of" labels. */
export function ago(iso?: string | null, now = Date.now()): string {
  if (!iso) return "";
  const a = age(iso, now);
  if (a === "now") return "just now";
  const unit = { m: "minute", h: "hour", d: "day" }[a.slice(-1)] ?? "";
  const n = Number(a.slice(0, -1));
  return `${n} ${unit}${n === 1 ? "" : "s"} ago`;
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** Strips terminal escapes and applies carriage-return overwrites, like a terminal would. */
export function cleanLog(text: string): string {
  const noAnsi = text.replace(
    /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-Za-z]/g,
    "",
  );
  return noAnsi
    .split("\n")
    .map((line) => {
      const parts = line.split("\r");
      return parts.length > 1 ? (parts.filter((p) => p !== "").at(-1) ?? "") : line;
    })
    .join("\n");
}

/** Same rules as pier for branch names: what a create accepts. */
export function branchProblem(b: string): string | null {
  if (!b) return "a branch name is required";
  if (b.length > 100) return "at most 100 characters";
  if (!/^[A-Za-z0-9._/-]+$/.test(b)) return "letters, digits, . _ - and / only";
  if (/^[-./]/.test(b)) return `can't start with ${b[0]}`;
  if (/[/.]$/.test(b) || b.endsWith(".lock")) return "can't end with / . or .lock";
  if (b.includes("..") || b.includes("//")) return "can't contain .. or //";
  return null;
}
