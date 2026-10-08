// Edits not saved to the session yet, kept with the app's storage on every
// change: an app that quits or crashes with a file half edited opens it as
// it was left.
import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface Buffer {
  text: string;
  /** The sha of the session's file the edit started from ("" for a new file). */
  baseSha: string;
}

interface Buffers {
  buffers: Record<string, Buffer>;
  put: (sessionId: string, path: string, b: Buffer) => void;
  drop: (sessionId: string, path: string) => void;
}

export const bufferKey = (sessionId: string, path: string) => `${sessionId}\n${path}`;

export const useBuffers = create<Buffers>()(
  persist(
    (set) => ({
      buffers: {},
      put: (sessionId, path, b) =>
        set((s) => ({ buffers: { ...s.buffers, [bufferKey(sessionId, path)]: b } })),
      drop: (sessionId, path) =>
        set((s) => {
          const { [bufferKey(sessionId, path)]: _, ...rest } = s.buffers;
          return { buffers: rest };
        }),
    }),
    { name: "pier.buffers" },
  ),
);

/** How many files have unsaved edits, in all sessions. */
export function unsavedCount(): number {
  return Object.keys(useBuffers.getState().buffers).length;
}
