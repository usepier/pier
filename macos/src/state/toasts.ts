import { create } from "zustand";
import { RpcError } from "@/api/rpc";

export interface Toast {
  id: number;
  tone: "ok" | "bad" | "info";
  text: string;
}

interface Toasts {
  items: Toast[];
  push: (t: Omit<Toast, "id">) => void;
  dismiss: (id: number) => void;
}

let next = 0;

export const useToasts = create<Toasts>((set) => ({
  items: [],
  push: (t) => {
    const id = ++next;
    set((s) => ({ items: [...s.items.slice(-3), { ...t, id }] }));
    setTimeout(
      () => set((s) => ({ items: s.items.filter((x) => x.id !== id) })),
      t.tone === "bad" ? 8000 : 4000,
    );
  },
  dismiss: (id) => set((s) => ({ items: s.items.filter((x) => x.id !== id) })),
}));

export function toast(text: string, tone: Toast["tone"] = "info") {
  useToasts.getState().push({ text, tone });
}

export function toastError(e: unknown) {
  if (e instanceof RpcError && e.kind === "cancelled") return;
  toast(e instanceof Error ? e.message : String(e), "bad");
}

/** Runs an action, toasting its failure. */
export async function attempt<T>(f: () => Promise<T>): Promise<T | undefined> {
  try {
    return await f();
  } catch (e) {
    toastError(e);
    return undefined;
  }
}
