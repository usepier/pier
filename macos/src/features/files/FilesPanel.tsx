import { useQuery } from "@tanstack/react-query";
import { ChevronRight, File, Folder, Search } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { rank } from "@/features/chat/mentions";
import { plural } from "@/lib/format";
import { docId, useDocs } from "@/state/docs";
import { useInspector } from "@/state/inspector";
import { Spinner } from "@/ui/Spinner";
import { buildTree, type Node } from "./tree";

function running(s: Session) {
  return ["running", "working", "idle"].includes(s.state) && s.hasKey;
}

const changeTone: Record<string, string> = {
  added: "text-ok",
  untracked: "text-ok",
  modified: "text-warn",
  renamed: "text-info",
  conflicted: "text-bad",
};

/** The session's checkout: a tree to browse, a filter, and a search of the contents. */
export function FilesPanel({ session: s }: { session: Session }) {
  const mode = useInspector((i) => i.filesMode);
  const setMode = useInspector((i) => i.setFilesMode);
  if (!running(s)) {
    return (
      <div className="p-4 text-sm text-secondary">
        {s.state === "parked" ? "Parked — wake it to browse its files." : "Not readable right now."}
      </div>
    );
  }
  return (
    <div className="flex h-full flex-col">
      <fieldset
        aria-label="Show"
        className="m-0 flex shrink-0 gap-1 border-0 border-b border-separator p-1.5 text-xs"
      >
        {(
          [
            ["tree", "Files"],
            ["search", "Search"],
          ] as const
        ).map(([m, title]) => (
          <button
            key={m}
            type="button"
            aria-pressed={mode === m}
            onClick={() => setMode(m)}
            className={`h-6 flex-1 rounded-ctl ${mode === m ? "bg-selected text-label" : "text-secondary hover:bg-hover"}`}
          >
            {title}
          </button>
        ))}
      </fieldset>
      {mode === "tree" ? <Tree session={s} /> : <ContentSearch session={s} />}
    </div>
  );
}

function Tree({ session: s }: { session: Session }) {
  const files = useQuery({
    queryKey: ["files", s.id],
    queryFn: () => rpc.call("files.list", { sessionId: s.id }),
    staleTime: 30_000,
  });
  const changes = useQuery({
    queryKey: ["changes", s.id],
    queryFn: () => rpc.call("git.changes", { id: s.id }),
    staleTime: 5_000,
  });
  const status = useMemo(
    () => new Map((changes.data?.files ?? []).map((f) => [f.path, f.status])),
    [changes.data],
  );
  const [filter, setFilter] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const openDoc = useDocs((d) => d.open);
  const active = useDocs((d) => d.active[s.id]);
  const paths = files.data?.paths ?? [];
  const tree = useMemo(() => buildTree(paths), [paths]);
  const filtered = useMemo(() => (filter ? rank(paths, filter, 200) : null), [paths, filter]);

  const row = (n: Node, depth: number): ReactNode => {
    if (n.children) {
      const isOpen = open.has(n.path);
      return (
        <li key={n.path}>
          <button
            type="button"
            aria-expanded={isOpen}
            onClick={() =>
              setOpen((o) => {
                const next = new Set(o);
                if (isOpen) next.delete(n.path);
                else next.add(n.path);
                return next;
              })
            }
            style={{ paddingLeft: 8 + depth * 12 }}
            className="flex h-6 w-full items-center gap-1 pr-2 text-left text-sm hover:bg-hover"
          >
            <ChevronRight
              size={11}
              className={`shrink-0 text-tertiary transition-transform ${isOpen ? "rotate-90" : ""}`}
            />
            <Folder size={13} className="shrink-0 text-secondary" />
            <span className="truncate">{n.name}</span>
          </button>
          {isOpen && <ul>{n.children.map((c) => row(c, depth + 1))}</ul>}
        </li>
      );
    }
    return <li key={n.path}>{fileButton(n.path, n.name, 20 + depth * 12)}</li>;
  };

  const fileButton = (path: string, label: ReactNode, pad: number) => {
    const st = status.get(path);
    const on = active === docId("file", path);
    return (
      <button
        type="button"
        onClick={() => openDoc(s.id, { kind: "file", path })}
        style={{ paddingLeft: pad }}
        title={path}
        className={`flex h-6 w-full items-center gap-1.5 pr-2 text-left text-sm ${on ? "bg-selected" : "hover:bg-hover"}`}
      >
        <File size={12} className="shrink-0 text-tertiary" />
        <span className={`truncate ${st ? (changeTone[st] ?? "") : ""}`}>{label}</span>
      </button>
    );
  };

  return (
    <>
      <div className="shrink-0 p-1.5">
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter files"
          aria-label="Filter files"
          className="selectable h-6 w-full rounded-ctl border border-separator bg-control px-2 text-sm outline-none placeholder:text-tertiary focus:border-accent"
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-2">
        {!files.data ? (
          <div className="flex justify-center p-4">
            {files.error ? (
              <span className="text-sm text-bad">{(files.error as Error).message}</span>
            ) : (
              <Spinner />
            )}
          </div>
        ) : filtered ? (
          <ul>
            {filtered.map((p) => (
              <li key={p}>{fileButton(p, p, 10)}</li>
            ))}
          </ul>
        ) : (
          <ul>{tree.map((n) => row(n, 0))}</ul>
        )}
        {files.data?.truncated && (
          <div className="px-3 py-1 text-2xs text-tertiary">Showing the first {paths.length} files.</div>
        )}
      </div>
    </>
  );
}

function ContentSearch({ session: s }: { session: Session }) {
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [regex, setRegex] = useState(false);
  const [caseSensitive, setCase] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const focus = useInspector((i) => i.focusSearch);
  const openDoc = useDocs((d) => d.open);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(query), 250);
    return () => clearTimeout(t);
  }, [query]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: each request to search focuses the field
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, [focus]);
  const result = useQuery({
    queryKey: ["search", s.id, debounced, regex, caseSensitive],
    queryFn: ({ signal }) =>
      rpc.call("files.search", { sessionId: s.id, query: debounced, regex, caseSensitive }, { signal }),
    enabled: debounced.length > 0,
    staleTime: 10_000,
  });
  const groups = useMemo(() => {
    const m = new Map<string, NonNullable<typeof result.data>["matches"]>();
    for (const x of result.data?.matches ?? []) m.set(x.path, [...(m.get(x.path) ?? []), x]);
    return [...m];
  }, [result.data]);
  const toggle = (on: boolean, set: (v: boolean) => void, label: string, text: string) => (
    <button
      type="button"
      aria-pressed={on}
      aria-label={label}
      title={label}
      onClick={() => set(!on)}
      className={`h-5 rounded px-1 font-mono text-[11px] ${on ? "bg-accent text-on-accent" : "text-secondary hover:bg-hover"}`}
    >
      {text}
    </button>
  );
  return (
    <>
      <div className="shrink-0 p-1.5">
        <div className="flex h-6 items-center gap-1 rounded-ctl border border-separator bg-control pr-1 pl-2 focus-within:border-accent">
          <Search size={12} className="shrink-0 text-tertiary" />
          <input
            ref={input}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search the checkout"
            aria-label="Search the checkout"
            className="selectable min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-tertiary"
          />
          {toggle(caseSensitive, setCase, "Match case", "Aa")}
          {toggle(regex, setRegex, "Regular expression", ".*")}
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-2 text-sm">
        {result.isFetching && !result.data && (
          <div className="flex justify-center p-4">
            <Spinner />
          </div>
        )}
        {result.error && (
          <div className="selectable px-3 py-2 text-bad">{(result.error as Error).message}</div>
        )}
        {result.data && (
          <div className="px-3 py-1 text-2xs text-tertiary">
            {plural(result.data.matches.length, "match", "matches")} in {plural(groups.length, "file")}
            {result.data.truncated ? " (more not shown)" : ""}
          </div>
        )}
        {groups.map(([path, ms]) => (
          <div key={path} className="mb-1">
            <div className="truncate px-3 py-0.5 text-xs font-medium text-secondary" title={path}>
              {path}
            </div>
            {ms.map((m) => (
              <button
                key={`${m.line}:${m.col}`}
                type="button"
                onClick={() => openDoc(s.id, { kind: "file", path, line: m.line })}
                className="flex w-full items-baseline gap-2 py-0.5 pr-2 pl-5 text-left hover:bg-hover"
              >
                <span className="w-7 shrink-0 text-right font-mono text-[10.5px] text-tertiary">
                  {m.line}
                </span>
                <span className="min-w-0 truncate font-mono text-[11.5px]">{m.text.trim()}</span>
              </button>
            ))}
          </div>
        ))}
      </div>
    </>
  );
}
