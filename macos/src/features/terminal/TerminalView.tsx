import { FitAddon, Ghostty, Terminal } from "ghostty-web";
import wasm from "ghostty-web/ghostty-vt.wasm?url";
import { useCallback, useEffect, useRef, useState } from "react";
import { rpc } from "@/api/client";
import type { LocalPurpose } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { attachSink, pause, resume, sendInput, sendResize } from "@/api/terminal";
import { useBackgroundExpired } from "@/state/background";
import { usePrefs } from "@/state/prefs";
import { Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";
import { optionAsMeta, wireText } from "./keys";
import { reportMouse } from "./mouse";

// Ghostty's terminal, compiled to WebAssembly: loaded once, from the app's
// own files, and shared by every tab.
const ghostty = Ghostty.load(wasm);

type Phase = "idle" | "connecting" | "attached" | "detached" | "ended" | "failed";

function palette(dark: boolean) {
  return dark
    ? { background: "#1c1c1e", foreground: "#e5e5e7", cursor: "#e5e5e7", selectionBackground: "#3a5a8c" }
    : { background: "#ffffff", foreground: "#1d1d1f", cursor: "#1d1d1f", selectionBackground: "#b3d1ff" };
}

/**
 * One terminal tab on a session's tmux window. It attaches only while it
 * matters: visible, or hidden for less than a minute; and never once the
 * app has been in the background a while. An attached terminal keeps the
 * session awake; a detached one lets it park, and reattaches instantly —
 * tmux redraws the window as it is.
 */
export function TerminalView({
  sessionId,
  window: windowId,
  visible,
  local,
  profile,
}: {
  sessionId?: string;
  window?: string;
  visible: boolean;
  /** A local terminal for one of pier's steps instead of a session window. */
  local?: LocalPurpose;
  /** The AWS profile an aws-sso login is for. */
  profile?: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const termId = useRef<string | null>(null);
  const unsink = useRef<(() => void) | null>(null);
  const [phase, setPhase] = useState<Phase>("idle");
  // Bumped for every terminal instance, so each one gets its own attach.
  const [instance, setInstance] = useState(0);
  const [error, setError] = useState("");
  const backgroundExpired = useBackgroundExpired();
  const hiddenDetachMs = usePrefs((p) => p.hiddenDetachMs);
  const fontSize = usePrefs((p) => p.terminalFontSize);

  // The terminal lives as long as the tab.
  useEffect(() => {
    let made: Terminal | undefined;
    let ro: ResizeObserver | undefined;
    let unwire: (() => void) | undefined;
    let gone = false;
    void ghostty.then(
      (g) => {
        if (gone) return;
        const dark = matchMedia("(prefers-color-scheme: dark)").matches;
        const t = new Terminal({
          fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
          fontSize: usePrefs.getState().terminalFontSize,
          cursorBlink: true,
          scrollback: 5000,
          theme: palette(dark),
          ghostty: g,
        });
        const f = new FitAddon();
        t.loadAddon(f);
        t.attachCustomKeyEventHandler((e) => {
          // ⌘-combos belong to the menu bar, not the shell. ghostty-web cancels
          // a key it's told to leave alone, which would keep it from the menu
          // too: its one call to preventDefault does nothing.
          if (e.metaKey) {
            e.preventDefault = () => void Reflect.deleteProperty(e, "preventDefault");
            return true;
          }
          const meta = optionAsMeta(e);
          if (meta) t.input(meta, true);
          return !!meta;
        });
        const unmouse = reportMouse(t, host.current!);
        t.open(host.current!);
        const untext = wireText(t, host.current!);
        unwire = () => {
          unmouse();
          untext();
        };
        t.onData((d) => termId.current && sendInput(termId.current, d));
        t.onResize(({ cols, rows }) => termId.current && sendResize(termId.current, cols, rows));
        made = t;
        term.current = t;
        fit.current = f;
        setInstance((n) => n + 1);
        ro = new ResizeObserver(() => {
          try {
            f.fit();
          } catch {
            // not laid out
          }
        });
        ro.observe(host.current!);
      },
      (e) => {
        if (gone) return;
        setPhase("failed");
        setError(`The terminal couldn't start: ${e instanceof Error ? e.message : String(e)}`);
      },
    );
    return () => {
      gone = true;
      ro?.disconnect();
      unwire?.();
      if (termId.current) void rpc.call("term.close", { termId: termId.current }).catch(() => {});
      unsink.current?.();
      unsink.current = null;
      termId.current = null;
      term.current = null;
      made?.dispose();
      setPhase("idle");
    };
  }, []);

  useEffect(() => {
    if (term.current) {
      term.current.options.fontSize = fontSize;
      fit.current?.fit();
    }
  }, [fontSize]);

  const detach = useCallback(() => {
    const id = termId.current;
    if (!id) return;
    termId.current = null;
    unsink.current?.();
    unsink.current = null;
    void rpc.call("term.close", { termId: id }).catch(() => {});
    setPhase("detached");
  }, []);

  const attach = useCallback(async () => {
    const t = term.current;
    if (!t || termId.current) return;
    setPhase("connecting");
    setError("");
    try {
      fit.current?.fit();
    } catch {
      // not laid out yet; the server's default size will do until a resize
    }
    try {
      const opened = local
        ? await rpc.call("term.openLocal", { purpose: local, profile, cols: t.cols, rows: t.rows })
        : await rpc.call("term.open", {
            sessionId: sessionId!,
            window: windowId,
            cols: t.cols,
            rows: t.rows,
          });
      t.reset();
      termId.current = opened.termId;
      unsink.current = attachSink(opened.termId, {
        output: (bytes, done) => t.write(bytes, done),
        exit: () => {
          termId.current = null;
          setPhase("ended");
        },
      });
      setPhase("attached");
      t.focus();
    } catch (e) {
      setPhase("failed");
      setError(e instanceof RpcError ? e.message : String(e));
    }
  }, [local, profile, sessionId, windowId]);

  // Attach when it matters, pause and later detach when it doesn't.
  useEffect(() => {
    if (!instance) return;
    if (backgroundExpired) {
      detach();
      return;
    }
    if (visible) {
      if (termId.current) resume(termId.current);
      else if (phase !== "connecting" && phase !== "failed" && phase !== "ended") void attach();
      term.current?.focus();
      return;
    }
    if (!termId.current) return;
    pause(termId.current);
    const timer = setTimeout(detach, hiddenDetachMs);
    return () => clearTimeout(timer);
  }, [instance, visible, backgroundExpired, phase, attach, detach, hiddenDetachMs]);

  return (
    <div
      className="relative h-full w-full bg-[#ffffff] dark:bg-[#1c1c1e]"
      style={{ display: visible ? "block" : "none" }}
    >
      <div ref={host} className="absolute inset-0 m-2" />
      {phase === "connecting" && (
        <div className="absolute inset-x-0 top-3 flex justify-center">
          <span className="flex items-center gap-2 rounded-full bg-raised px-3 py-1 text-xs text-secondary shadow">
            <Spinner /> attaching…
          </span>
        </div>
      )}
      {(phase === "ended" || phase === "failed") && (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-surface/85 text-sm">
          <div className="max-w-md text-center text-secondary">
            {phase === "ended" ? "This terminal ended." : error}
          </div>
          <Button onPress={() => void attach()}>{local ? "Run again" : "Reattach"}</Button>
        </div>
      )}
    </div>
  );
}
