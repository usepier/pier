// Every action the app offers is one command: the menu bar, the command
// palette, context menus and keyboard shortcuts all come from this registry,
// so an action is defined once.
import { useSyncExternalStore } from "react";

export type MenuSection = "app" | "file" | "edit" | "view" | "session" | "repo" | "go" | "help";

export interface Command {
  id: string;
  title: string;
  /** Tauri accelerator syntax, e.g. "CmdOrCtrl+Shift+P". */
  shortcut?: string;
  /** The menu it appears in; omit for palette-only commands. */
  menu?: MenuSection;
  /** Words the palette also matches. */
  keywords?: string;
  /** Whether it applies right now; disabled commands stay in menus, greyed. */
  enabled?: () => boolean;
  run: () => void | Promise<void>;
}

const registry = new Map<string, Command>();
const listeners = new Set<() => void>();
let version = 0;

function changed() {
  version++;
  for (const l of listeners) l();
}

/** Registers commands; the returned function removes them. */
export function registerCommands(cmds: Command[]): () => void {
  for (const c of cmds) registry.set(c.id, c);
  changed();
  return () => {
    for (const c of cmds) if (registry.get(c.id) === c) registry.delete(c.id);
    changed();
  };
}

export function allCommands(): Command[] {
  return [...registry.values()];
}

export function runCommand(id: string) {
  const c = registry.get(id);
  if (c && (c.enabled?.() ?? true)) void c.run();
}

export function useCommandsVersion(): number {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => version,
  );
}

/** Matches a keyboard event against an accelerator (browser fallback; the native menu handles them in the app). */
export function matchesShortcut(e: KeyboardEvent, shortcut: string): boolean {
  const parts = shortcut.split("+");
  const key = parts.pop()!.toLowerCase();
  const want = new Set(parts.map((p) => p.toLowerCase()));
  const cmd = want.has("cmdorctrl") || want.has("cmd") || want.has("command");
  if (cmd !== (e.metaKey || e.ctrlKey)) return false;
  if (want.has("shift") !== e.shiftKey) return false;
  if ((want.has("alt") || want.has("option")) !== e.altKey) return false;
  const pressed = e.key.length === 1 ? e.key.toLowerCase() : e.key.toLowerCase();
  const code = e.code.startsWith("Key")
    ? e.code.slice(3).toLowerCase()
    : e.code.startsWith("Digit")
      ? e.code.slice(5)
      : "";
  return pressed === key || code === key || (key === "," && pressed === ",");
}
