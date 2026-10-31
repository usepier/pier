// Confirmations are the app's own modals, never the system's alerts: a
// title, what it means, and the choices as buttons (ConfirmHost draws
// them). Each call waits for its answer.
import { create } from "zustand";

export interface Asking {
  id: number;
  title: string;
  text: string;
  /** The choices, first the one that goes ahead; the last one cancels. */
  choices: string[];
  destructive: boolean;
  answer: (choice: number) => void;
}

export const useAsking = create<{ asks: Asking[] }>(() => ({ asks: [] }));

let next = 0;

function put(title: string, text: string, choices: string[], destructive: boolean): Promise<number> {
  return new Promise((resolve) => {
    const id = ++next;
    const answer = (choice: number) => {
      useAsking.setState((s) => ({ asks: s.asks.filter((a) => a.id !== id) }));
      resolve(choice);
    };
    useAsking.setState((s) => ({ asks: [...s.asks, { id, title, text, choices, destructive, answer }] }));
  });
}

/** Asks to go ahead; resolves true when confirmed. */
export async function confirm(
  title: string,
  message: string,
  opts: { ok?: string; destructive?: boolean } = {},
): Promise<boolean> {
  return (await put(title, message, [opts.ok ?? "OK", "Cancel"], !!opts.destructive)) === 0;
}

/**
 * Asks with two or three choices (the last one cancels); resolves the index
 * of the one taken.
 */
export function ask(
  title: string,
  text: string,
  choices: [string, string] | [string, string, string],
): Promise<number> {
  return put(title, text, choices, false);
}
