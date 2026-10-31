import { FitAddon } from "@xterm/addon-fit";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { openUrl } from "@tauri-apps/plugin-opener";
import { useCallback, useEffect, useRef, useState } from "react";
import { rpc } from "@/api/client";
import type { LocalPurpose } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { attachSink, pause, resume, sendInput, sendResize } from "@/api/terminal";
import { inTauri } from "@/api/transport";
import { useBackgroundExpired } from "@/state/background";
import { usePrefs } from "@/state/prefs";
import { Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";

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
  // Bumped for every xterm instance, so each one gets its own attach.
  const [instance, setInstance] = useState(0);
  const [error, setError] = useState("");
  const backgroundExpired = useBackgroundExpired();
  const hiddenDetachMs = usePrefs((p) => p.hiddenDetachMs);
  const fontSize = usePrefs((p) => p.terminalFontSize);

  // The xterm instance lives as long as the tab.
  useEffect(() => {
    const dark = matchMedia("(prefers-color-scheme: dark)").matches;
    const t = new Terminal({
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: usePrefs.getState().terminalFontSize,
      lineHeight: 1.15,
      cursorBlink: true,
      allowProposedApi: true,
      macOptionIsMeta: true,
      scrollback: 5000,
      theme: palette(dark),
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.loadAddon(new Unicode11Addon());
    t.unicode.activeVersion = "11";
    t.loadAddon(new WebLinksAddon((_e, uri) => (inTauri ? void openUrl(uri) : window.open(uri, "_blank"))));
    // ⌘-combos belong to the menu bar, not the shell.
    t.attachCustomKeyEventHandler((e) => !e.metaKey);
    t.open(host.current!);
    try {
      const gl = new WebglAddon();
      gl.onContextLoss(() => gl.dispose());
      t.loadAddon(gl);
    } catch {
      // the DOM renderer it is
    }
    t.onData((d) => termId.current && sendInput(termId.current, d));
    t.onBinary(
      (d) =>
        termId.current &&
        sendInput(
          termId.current,
          Uint8Array.from(d, (c) => c.charCodeAt(0)),
        ),
    );
    term.current = t;
    fit.current = f;
    setInstance((n) => n + 1);
    const ro = new ResizeObserver(() => {
      try {
        f.fit();
      } catch {
        return;
      }
      if (termId.current) sendResize(termId.current, t.cols, t.rows);
    });
    ro.observe(host.current!);
    return () => {
      ro.disconnect();
      if (termId.current) void rpc.call("term.close", { termId: termId.current }).catch(() => {});
      unsink.current?.();
      unsink.current = null;
      termId.current = null;
      term.current = null;
      t.dispose();
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
