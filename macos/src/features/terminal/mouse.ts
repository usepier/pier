// Mouse reporting, which ghostty-web leaves out: while the program asks for
// the mouse (tmux with its mouse mode on), the wheel, clicks and drags go to
// it as reports instead of scrolling or selecting here. ⇧ keeps a click or
// drag here, to select text; ⌘ keeps a click here, to open a link.
import type { Terminal } from "ghostty-web";

const SGR = 1006;
const DRAG = 1002;
const ANY = 1003;
const WHEEL_UP = 64;
const WHEEL_DOWN = 65;
const MOTION = 32;

/** Wires mouse reporting into t, whose element is host; returns the undo. */
export function reportMouse(t: Terminal, host: HTMLElement): () => void {
  const tracking = () => t.wasmTerm?.hasMouseTracking() ?? false;
  const mode = (m: number) => t.wasmTerm?.getMode(m) ?? false;

  // The cell under the pointer, 1-based and kept on the grid.
  const cell = (e: MouseEvent) => {
    const canvas = host.querySelector("canvas");
    const m = t.renderer?.getMetrics();
    if (!canvas || !m) return { x: 1, y: 1 };
    const r = canvas.getBoundingClientRect();
    const clamp = (n: number, max: number) => Math.min(Math.max(n, 1), max);
    return {
      x: clamp(Math.floor((e.clientX - r.left) / m.width) + 1, t.cols),
      y: clamp(Math.floor((e.clientY - r.top) / m.height) + 1, t.rows),
    };
  };

  const send = (button: number, e: MouseEvent, release = false) => {
    const { x, y } = cell(e);
    const b = button | (e.altKey ? 8 : 0) | (e.ctrlKey ? 16 : 0);
    if (mode(SGR)) {
      t.input(`\x1b[<${b};${x};${y}${release ? "m" : "M"}`, true);
    } else if (x <= 223 && y <= 223) {
      // The old encoding: no release button, and nothing past column 223.
      t.input(`\x1b[M${String.fromCharCode(32 + (release ? 3 : b), 32 + x, 32 + y)}`, true);
    }
  };

  // Wheel: a report per line scrolled, the pixels in between carried over.
  let carry = 0;
  t.attachCustomWheelEventHandler((e) => {
    if (!tracking()) return false;
    const h = t.renderer?.getMetrics().height ?? 16;
    carry += e.deltaMode === WheelEvent.DOM_DELTA_PIXEL ? e.deltaY / h : e.deltaY;
    const lines = Math.trunc(carry);
    carry -= lines;
    for (let i = 0; i < Math.min(Math.abs(lines), 10); i++) send(lines < 0 ? WHEEL_UP : WHEEL_DOWN, e);
    return true;
  });

  // Clicks and drags. Listening on the host in the capture phase, ahead of
  // ghostty-web's own listeners (this runs before open()), keeps a reported
  // click from also starting a selection.
  let held: number | null = null;
  let last = "";
  const move = (e: MouseEvent) => {
    if (held === null || !(mode(DRAG) || mode(ANY))) return;
    const { x, y } = cell(e);
    if (`${x};${y}` === last) return;
    last = `${x};${y}`;
    send(held | MOTION, e);
  };
  const up = (e: MouseEvent) => {
    if (held === null) return;
    send(held, e, true);
    held = null;
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
  };
  const down = (e: MouseEvent) => {
    if (!tracking() || e.shiftKey || e.metaKey || e.button > 2) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    t.focus();
    if (held !== null) return;
    held = e.button;
    last = "";
    send(held, e);
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  };
  const menu = (e: MouseEvent) => {
    if (tracking() && !e.shiftKey) e.preventDefault();
  };
  host.addEventListener("mousedown", down, { capture: true });
  host.addEventListener("contextmenu", menu);
  return () => {
    host.removeEventListener("mousedown", down, { capture: true });
    host.removeEventListener("contextmenu", menu);
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
  };
}
