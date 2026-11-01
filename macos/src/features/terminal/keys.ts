// Keys and text the terminal handles itself, where ghostty-web would do
// something else or nothing.
import type { Terminal } from "ghostty-web";

const PUNCTUATION: Record<string, [string, string]> = {
  Minus: ["-", "_"],
  Equal: ["=", "+"],
  BracketLeft: ["[", "{"],
  BracketRight: ["]", "}"],
  Backslash: ["\\", "|"],
  Semicolon: [";", ":"],
  Quote: ["'", '"'],
  Comma: [",", "<"],
  Period: [".", ">"],
  Slash: ["/", "?"],
  Backquote: ["`", "~"],
  Space: [" ", " "],
};

/**
 * ⌥ is Meta, as with Terminal's "Use Option as Meta key": ESC, then the key
 * as it reads without ⌥ (on a US layout) — so ⌥B, ⌥F, ⌥. and ⌥⌫ work in the
 * shell rather than typing ∫, ƒ, ≥. Arrows and the like are left to the
 * terminal, which already sends them with Alt.
 */
export function optionAsMeta(e: KeyboardEvent): string | undefined {
  if (!e.altKey || e.ctrlKey || e.metaKey) return;
  if (e.code === "Backspace") return "\x1b\x7f";
  let key: string | undefined;
  if (/^Key[A-Z]$/.test(e.code)) key = e.shiftKey ? e.code.slice(3) : e.code.slice(3).toLowerCase();
  else if (/^Digit\d$/.test(e.code) && !e.shiftKey) key = e.code.slice(5);
  else key = PUNCTUATION[e.code]?.[e.shiftKey ? 1 : 0];
  return key && `\x1b${key}`;
}

/**
 * Text in and out that ghostty-web leaves alone: what comes without a key
 * (the emoji picker, dictation) goes to the program — composed text it
 * sends itself — and Copy (⌘C, Edit ▸ Copy) takes the selection. Returns
 * the undo.
 */
export function wireText(t: Terminal, host: HTMLElement): () => void {
  const insert = (e: InputEvent) => {
    if (e.inputType === "insertText" && e.data && !e.isComposing) t.input(e.data, true);
  };
  const copy = (e: ClipboardEvent) => {
    if (!t.hasSelection()) return;
    e.clipboardData?.setData("text/plain", t.getSelection());
    e.preventDefault();
  };
  host.addEventListener("beforeinput", insert);
  host.addEventListener("copy", copy);
  return () => {
    host.removeEventListener("beforeinput", insert);
    host.removeEventListener("copy", copy);
  };
}
