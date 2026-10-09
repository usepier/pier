// The accent color follows pier's `theme` setting, shared with the terminal
// app. fill is the accent under text (a primary button): a shade that fg,
// the text drawn on it, reads on at 4.5:1 or more.
const accents: Record<string, { color: string; fill: string; fg: string }> = {
  teal: { color: "#13a596", fill: "#0d7d71", fg: "#ffffff" },
  navy: { color: "#2b4fc5", fill: "#2b4fc5", fg: "#ffffff" },
  violet: { color: "#7c3aed", fill: "#7c3aed", fg: "#ffffff" },
  emerald: { color: "#10b981", fill: "#047857", fg: "#ffffff" },
  orange: { color: "#f97316", fill: "#c2410c", fg: "#ffffff" },
  crimson: { color: "#e11d48", fill: "#e11d48", fg: "#ffffff" },
  pink: { color: "#ec4899", fill: "#be185d", fg: "#ffffff" },
  amber: { color: "#f59e0b", fill: "#f59e0b", fg: "#1d1d1f" },
  graphite: { color: "#8e8e93", fill: "#636366", fg: "#ffffff" },
};

export function applyAccent(theme: string | undefined) {
  const a = accents[theme ?? ""] ?? accents.teal!;
  const root = document.documentElement.style;
  root.setProperty("--accent", a.color);
  root.setProperty("--accent-fill", a.fill);
  root.setProperty("--on-accent", a.fg);
}

export function accentColor(name: string): string {
  return accents[name]?.color ?? accents.teal!.color;
}
