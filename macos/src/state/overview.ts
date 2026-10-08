// The overview — sessions, repos, totals — as one live subscription the
// whole app shares, plus telling pier when the window is looked at so it
// polls briskly or lazily.
import { useEffect } from "react";
import { rpc } from "@/api/client";
import { useStatus, useWatch } from "@/api/live";
import type { Overview, Repo, Session } from "@/api/protocol.gen";
import { recordLoad } from "./history";

export function useOverview(): Overview | undefined {
  return useWatch("overview", {}, { graceMs: 120_000 }).data;
}

/** Feeds every refresh's load readings into the sparkline history. */
export function useLoadRecorder() {
  const o = useOverview();
  useEffect(() => {
    for (const s of o?.sessions ?? []) recordLoad(s.id, s.resources);
  }, [o]);
}

export function useRepo(name: string): Repo | undefined {
  return useOverview()?.repos.find((r) => r.name === name);
}

export function useSession(id: string): Session | undefined {
  return useOverview()?.sessions.find((s) => s.id === id);
}

export function useRepoSessions(name: string): Session[] {
  const o = useOverview();
  return (o?.sessions ?? [])
    .filter((s) => s.repo === name && !s.pooled)
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** Reports window focus to pier while the app runs. */
export function useActivityReporting() {
  const status = useStatus();
  useEffect(() => {
    if (status !== "ready") return;
    const send = () => rpc.notify("client.activity", { focused: document.hasFocus() && !document.hidden });
    send();
    window.addEventListener("focus", send);
    window.addEventListener("blur", send);
    document.addEventListener("visibilitychange", send);
    return () => {
      window.removeEventListener("focus", send);
      window.removeEventListener("blur", send);
      document.removeEventListener("visibilitychange", send);
    };
  }, [status]);
}

/** A session's place in the lifecycle, for dots and badges. */
export type Tone = "ok" | "busy" | "idle" | "parked" | "starting" | "bad" | "gone";

export function sessionTone(s: Session): Tone {
  switch (s.state) {
    case "creating":
      return "starting";
    case "working":
      return "busy";
    case "running":
      return s.setup === "failed" ? "bad" : "ok";
    case "idle":
      return "idle";
    case "parked":
      return "parked";
    case "failed":
      return "bad";
    default:
      return "gone";
  }
}
