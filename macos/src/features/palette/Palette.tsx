import { useNavigate } from "@tanstack/react-router";
import { Command as CommandIcon, FolderGit2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Dialog, Modal, ModalOverlay } from "react-aria-components";
import { create } from "zustand";
import { allCommands } from "@/app/commands";
import { sessionTone, useOverview } from "@/state/overview";
import { StateDot } from "@/ui/StateDot";
import { type Entry, rankEntries } from "./rank";

export const usePalette = create<{
  open: boolean;
  query: string;
  show: (query?: string) => void;
  hide: () => void;
}>((set) => ({
  open: false,
  query: "",
  show: (query = "") => set({ open: true, query }),
  hide: () => set({ open: false }),
}));

function prettyShortcut(s?: string) {
  if (!s) return "";
  return s
    .replace("CmdOrCtrl", "⌘")
    .replace("Shift", "⇧")
    .replace("Alt", "⌥")
    .replace("Ctrl", "⌃")
    .replaceAll("+", "");
}

/** ⌘K: go to any session or repository, or run any action, by typing part of its name. */
export function Palette() {
  const { open, query: initial, hide } = usePalette();
  const [query, setQuery] = useState("");
  const [pick, setPick] = useState(0);
  const o = useOverview();
  const navigate = useNavigate();
  useEffect(() => {
    if (open) {
      setQuery(initial);
      setPick(0);
    }
  }, [open, initial]);

  const entries = useMemo<Entry[]>(() => {
    if (!open) return [];
    const out: Entry[] = [];
    for (const s of o?.sessions ?? []) {
      if (s.pooled) continue;
      out.push({
        id: `s:${s.id}`,
        kind: "session",
        title: s.name,
        detail: `${s.repo} · ${s.activity || s.label}`,
        keywords: s.branch,
        run: () => navigate({ to: "/session/$id", params: { id: s.id } }),
      });
    }
    for (const r of o?.repos ?? []) {
      out.push({
        id: `r:${r.name}`,
        kind: "repo",
        title: r.name,
        detail: "repository",
        keywords: r.origin,
        run: () => navigate({ to: "/repo/$name", params: { name: r.name } }),
      });
    }
    for (const c of allCommands()) {
      if (c.id === "go.palette" || !(c.enabled?.() ?? true)) continue;
      out.push({
        id: `c:${c.id}`,
        kind: "command",
        title: c.title.replace(/…$/, ""),
        keywords: c.keywords,
        shortcut: c.shortcut,
        run: () => void c.run(),
      });
    }
    return out;
  }, [open, o, navigate]);
  const list = rankEntries(entries, query);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new query starts at the top match
  useEffect(() => setPick(0), [query]);
  const sessions = new Map((o?.sessions ?? []).map((s) => [s.id, s]));
  const choose = (e?: Entry) => {
    if (!e) return;
    hide();
    e.run();
  };
  return (
    <ModalOverlay
      isOpen={open}
      onOpenChange={(v) => !v && hide()}
      isDismissable
      className="fixed inset-0 z-50 flex justify-center bg-black/10"
    >
      <Modal className="mt-[90px] w-[600px] self-start overflow-hidden rounded-card border border-separator bg-surface shadow-2xl outline-none">
        <Dialog aria-label="Go to or run" className="outline-none">
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const n = list.length || 1;
                setPick((p) => (p + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
              }
              if (e.key === "Enter") choose(list[pick]);
            }}
            placeholder="Go to a session or repository — or type > for actions"
            aria-label="Search"
            className="selectable h-11 w-full border-b border-separator bg-transparent px-4 text-md outline-none placeholder:text-tertiary"
          />
          <div role="listbox" aria-label="Results" className="max-h-[55vh] overflow-y-auto py-1">
            {list.map((e, i) => {
              const on = i === pick;
              const s = e.kind === "session" ? sessions.get(e.id.slice(2)) : undefined;
              return (
                <div
                  key={e.id}
                  role="option"
                  tabIndex={-1}
                  aria-selected={on}
                  onMouseEnter={() => setPick(i)}
                  onMouseDown={(ev) => {
                    ev.preventDefault();
                    choose(e);
                  }}
                  className={`flex h-8 items-center gap-2.5 px-4 text-sm ${on ? "bg-accent text-on-accent" : ""}`}
                >
                  {s ? (
                    <StateDot tone={sessionTone(s)} />
                  ) : e.kind === "repo" ? (
                    <FolderGit2 size={13} className="shrink-0 opacity-70" />
                  ) : (
                    <CommandIcon size={13} className="shrink-0 opacity-70" />
                  )}
                  <span className="shrink-0 font-medium">{e.title}</span>
                  {e.detail && (
                    <span className={`min-w-0 truncate text-xs ${on ? "opacity-80" : "text-tertiary"}`}>
                      {e.detail}
                    </span>
                  )}
                  <span className="flex-1" />
                  {e.shortcut && (
                    <span className={`shrink-0 text-xs ${on ? "opacity-80" : "text-tertiary"}`}>
                      {prettyShortcut(e.shortcut)}
                    </span>
                  )}
                </div>
              );
            })}
            {list.length === 0 && <div className="px-4 py-2 text-sm text-secondary">Nothing matches.</div>}
          </div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  );
}
