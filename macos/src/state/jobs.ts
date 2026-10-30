// Background jobs (creates, bakes, fills, resizes) the app started or found
// running, and live progress for each.
import { useEffect } from "react";
import { create } from "zustand";
import { rpc } from "@/api/client";
import { useStatus, useWatch } from "@/api/live";
import type { Job, JobUpdate } from "@/api/protocol.gen";
import { attempt, toast } from "./toasts";

interface Jobs {
  jobs: Record<string, Job>;
  upsert: (j: Job) => void;
}

export const useJobs = create<Jobs>((set) => ({
  jobs: {},
  // A job that ended stays ended: a view still holding an older look at it
  // (one that remounts, a watch that lagged) can't bring it back running.
  upsert: (j) =>
    set((s) => {
      const had = s.jobs[j.id];
      if (had && had.state !== "running" && j.state === "running") return s;
      return { jobs: { ...s.jobs, [j.id]: j } };
    }),
}));

const kindTitle: Record<string, string> = {
  bake: "The bake",
  fill: "Filling the pool",
  resize: "The resize",
};

const followed = new Set<string>();

/**
 * Keeps a job in the list and follows it to its end, wherever the user is
 * by then: a failure says why. (A create that fails shows as its session
 * instead.)
 */
export function trackJob(j: Job) {
  useJobs.getState().upsert(j);
  if (j.state !== "running" || followed.has(j.id)) return;
  followed.add(j.id);
  const w = rpc.watch("job", { id: j.id }, (d) => {
    const job = (d as JobUpdate).job;
    useJobs.getState().upsert(job);
    if (job.state === "running") return;
    w.close();
    followed.delete(j.id);
    if ((job.state === "failed" || job.state === "lost") && job.kind !== "create") {
      const what = `${kindTitle[job.kind] ?? "A background job"}${job.repo ? ` of ${job.repo}` : ""}`;
      toast(`${what} failed: ${job.error || "its log says why"}`, "bad");
    }
  });
}

/** Starts a background job and follows it (trackJob). A refusal up front is a toast. */
export async function startJob(start: () => Promise<Job>): Promise<Job | undefined> {
  const j = await attempt(start);
  if (j) trackJob(j);
  return j;
}

/** Loads the jobs still running from before the app (re)started. */
export function useJobsBootstrap() {
  const status = useStatus();
  useEffect(() => {
    if (status !== "ready") return;
    void rpc.call("jobs.list", {}).then((list) => {
      for (const j of list) if (j.state === "running") trackJob(j);
    });
  }, [status]);
  // While any job shows running, pier's own list is asked now and then:
  // whatever a watch missed (a job handed between processes, a dropped
  // connection), the list says how each ended.
  const running = useJobs((s) => Object.values(s.jobs).some((j) => j.state === "running"));
  useEffect(() => {
    if (status !== "ready" || !running) return;
    const t = setInterval(() => {
      void rpc
        .call("jobs.list", {})
        .then((list) => {
          const have = useJobs.getState().jobs;
          for (const j of list) if (have[j.id] && have[j.id]!.state !== j.state) useJobs.getState().upsert(j);
        })
        .catch(() => {});
    }, jobsCheckMs);
    return () => clearInterval(t);
  }, [status, running]);
}

/** How often running jobs are checked against pier's list. */
const jobsCheckMs = 20_000;

/** A job with all its progress events so far, live. */
export function useJob(id: string | undefined): JobUpdate | undefined {
  const live = useWatch(
    "job",
    { id: id ?? "" },
    {
      enabled: !!id,
      reduce: (prev, next) => (prev ? { job: next.job, events: [...prev.events, ...next.events] } : next),
    },
  );
  const upsert = useJobs((s) => s.upsert);
  useEffect(() => {
    if (live.data) upsert(live.data.job);
  }, [live.data, upsert]);
  return id ? live.data : undefined;
}

export function useRunningJobs(filter?: (j: Job) => boolean): Job[] {
  const jobs = useJobs((s) => s.jobs);
  return Object.values(jobs).filter((j) => j.state === "running" && (!filter || filter(j)));
}
