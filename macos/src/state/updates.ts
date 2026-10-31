// Updates of the app (and the pier it carries): looked for every few
// hours, and on request (Pier ▸ Check for Updates…, Settings ▸ Updates).
import { relaunch } from "@tauri-apps/plugin-process";
import { check, type Update } from "@tauri-apps/plugin-updater";
import { create } from "zustand";
import { inTauri } from "@/api/transport";

interface Updates {
  update: Update | null;
  checking: boolean;
  checkedAt?: number;
  error?: string;
  /** Install progress, 0–1; undefined when not installing. */
  progress?: number;
  check: () => Promise<Update | null>;
  install: () => Promise<void>;
}

export const updatesAvailable = inTauri && !import.meta.env.DEV;

export const useUpdates = create<Updates>()((set, get) => ({
  update: null,
  checking: false,
  check: async () => {
    if (!updatesAvailable) {
      set({ error: "Updates come with the released app.", checkedAt: Date.now() });
      return null;
    }
    set({ checking: true, error: undefined });
    try {
      const u = await check();
      set({ update: u, checking: false, checkedAt: Date.now() });
      return u;
    } catch (e) {
      set({ checking: false, checkedAt: Date.now(), error: e instanceof Error ? e.message : String(e) });
      return null;
    }
  },
  install: async () => {
    const u = get().update;
    if (!u) return;
    let total = 0;
    let got = 0;
    set({ progress: 0 });
    try {
      await u.downloadAndInstall((e) => {
        if (e.event === "Started") total = e.data.contentLength ?? 0;
        if (e.event === "Progress") {
          got += e.data.chunkLength;
          if (total) set({ progress: got / total });
        }
      });
      await relaunch();
    } catch (e) {
      set({ progress: undefined, error: e instanceof Error ? e.message : String(e) });
      throw e;
    }
  },
}));

const every = 6 * 3600_000;

/** Looks for updates now and every few hours. */
export function watchUpdates(): () => void {
  if (!updatesAvailable) return () => {};
  const look = () => void useUpdates.getState().check();
  look();
  const t = setInterval(look, every);
  return () => clearInterval(t);
}
