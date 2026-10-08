import { useQuery } from "@tanstack/react-query";
import { File } from "lucide-react";
import { useEffect, useState } from "react";
import { Dialog, Modal, ModalOverlay } from "react-aria-components";
import { create } from "zustand";
import { rpc } from "@/api/client";
import { rank } from "@/features/chat/mentions";
import { splitPath } from "@/features/review/status";
import { useDocs } from "@/state/docs";

export const useQuickOpen = create<{ session?: string; show: (session: string) => void; hide: () => void }>(
  (set) => ({
    show: (session) => set({ session }),
    hide: () => set({ session: undefined }),
  }),
);

/** ⌘P: open a file of the session in view by typing part of its name. */
export function QuickOpen() {
  const session = useQuickOpen((q) => q.session);
  const hide = useQuickOpen((q) => q.hide);
  const [query, setQuery] = useState("");
  const [pick, setPick] = useState(0);
  const files = useQuery({
    queryKey: ["files", session],
    queryFn: () => rpc.call("files.list", { sessionId: session! }),
    enabled: !!session,
    staleTime: 30_000,
  });
  const matches = rank(files.data?.paths ?? [], query, 50);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new query starts at the top match
  useEffect(() => setPick(0), [query]);
  useEffect(() => {
    if (session) setQuery("");
  }, [session]);
  const choose = (path?: string) => {
    if (!path || !session) return;
    useDocs.getState().open(session, { kind: "file", path });
    hide();
  };
  return (
    <ModalOverlay
      isOpen={!!session}
      onOpenChange={(o) => !o && hide()}
      isDismissable
      className="fixed inset-0 z-50 flex justify-center bg-black/10"
    >
      <Modal className="mt-[90px] w-[560px] self-start overflow-hidden rounded-card border border-separator bg-surface shadow-2xl outline-none">
        <Dialog aria-label="Open a file" className="outline-none">
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const n = matches.length || 1;
                setPick((p) => (p + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
              }
              if (e.key === "Enter") choose(matches[pick]);
            }}
            placeholder="Open a file…"
            aria-label="File name"
            className="selectable h-11 w-full border-b border-separator bg-transparent px-4 text-md outline-none placeholder:text-tertiary"
          />
          <div role="listbox" aria-label="Files" className="max-h-[50vh] overflow-y-auto py-1">
            {matches.map((p, i) => {
              const [dir, name] = splitPath(p);
              return (
                <div
                  key={p}
                  role="option"
                  tabIndex={-1}
                  aria-selected={i === pick}
                  onMouseEnter={() => setPick(i)}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    choose(p);
                  }}
                  className={`flex h-7 items-center gap-2 px-4 text-sm ${i === pick ? "bg-accent text-on-accent" : ""}`}
                >
                  <File size={12} className="shrink-0 opacity-60" />
                  <span className="shrink-0">{name}</span>
                  <span className={`truncate text-xs ${i === pick ? "opacity-75" : "text-tertiary"}`}>
                    {dir}
                  </span>
                </div>
              );
            })}
            {files.data && matches.length === 0 && (
              <div className="px-4 py-2 text-sm text-secondary">No such file</div>
            )}
          </div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  );
}
