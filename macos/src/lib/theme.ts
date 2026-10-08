// The accent color follows pier's `theme` setting, shared with the terminal
// app. fg is the text color drawn on the accent.
const accents: Record<string, { color: string; fg: string }> = {
  teal: { color: "#13a596", fg: "#ffffff" },
  navy: { color: "#2b4fc5", fg: "#ffffff" },
  violet: { color: "#7c3aed", fg: "#ffffff" },
  emerald: { color: "#10b981", fg: "#ffffff" },
  orange: { color: "#f97316", fg: "#ffffff" },
  crimson: { color: "#e11d48", fg: "#ffffff" },
  pink: { color: "#ec4899", fg: "#ffffff" },
  amber: { color: "#f59e0b", fg: "#1d1d1f" },
  graphite: { color: "#8e8e93", fg: "#ffffff" },
};

export function applyAccent(theme: string | undefined) {
  const a = accents[theme ?? ""] ?? accents.teal!;
  const root = document.documentElement.style;
  root.setProperty("--accent", a.color);
  root.setProperty("--on-accent", a.fg);
}

export function accentColor(name: string): string {
  return accents[name]?.color ?? accents.teal!.color;
}
